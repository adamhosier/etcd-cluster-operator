package e2e

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/improbable-eng/etcd-cluster-operator/internal/test/try"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	etcd "go.etcd.io/etcd/client"
	"io/ioutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"os/exec"
	"path/filepath"
	kindv1alpha3 "sigs.k8s.io/kind/pkg/apis/config/v1alpha3"
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/create"
	"sigs.k8s.io/kind/pkg/container/cri"
	"strings"
	"testing"
	"time"
)

const (
	expectedClusterSize = 3
)

var (
	fUseKind           = flag.Bool("kind", false, "Creates a Kind cluster to run the tests against")
	fUseCurrentContext = flag.Bool("current-context", false, "Runs the tests against the current kubernetes context, the path to kube config defaults to ~/.kube/config, unless overridden by the KUBECONFIG environment variable")
	fRepoRoot          = flag.String("repo-root", "", "The absolute path to the root of the etcd-cluster-operator git repository.")
	fCleanup           = flag.Bool("cleanup", true, "Tears down the Kind cluster once the test is finished.")

	etcdConfig = etcd.Config{
		Endpoints: []string{"http://127.0.0.1:2379"},
		Transport: etcd.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
)

func TestE2E_Kind(t *testing.T) {
	if !*fUseKind {
		t.Skip()
	}

	// Tag for running this test, for naming resources.
	operatorImage := "etcd-operator:test"

	// Create Kind cluster to run the workloads.
	kind, stopKind := setupLocalCluster(t)
	defer stopKind()

	kubectl := &kubectlContext{
		t:          t,
		configPath: kind.KubeConfigPath(),
	}

	// Ensure CRDs exist in the cluster.
	t.Log("Applying CRDs")
	kubectl.Apply("--kustomize", filepath.Join(*fRepoRoot, "config", "crd"))

	// Build the operator.
	t.Log("Building operator image")
	out, err := exec.Command("docker", "build", "-t", operatorImage, *fRepoRoot).CombinedOutput()
	require.NoError(t, err, string(out))

	// Bundle the image to a tar.
	tmpDir, err := ioutil.TempDir("", "etcd-operator-e2e-test")
	require.NoError(t, err)
	imageTar := filepath.Join(tmpDir, "etcd-operator.tar")

	out, err = exec.Command("docker", "save", "-o", imageTar, operatorImage).CombinedOutput()
	require.NoError(t, err, string(out))
	imageFile, err := os.Open(imageTar)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, imageFile.Close(), "failed to close operator image tar")
	}()

	// Load the built image into the Kind cluster.
	t.Log("Loading image in to Kind cluster")
	nodes, err := kind.ListInternalNodes()
	require.NoError(t, err)
	for _, node := range nodes {
		err := node.LoadImageArchive(imageFile)
		require.NoError(t, err)
	}

	// Deploy the image, and ensure it starts.
	t.Log("Applying operator")
	kubectl.Apply("-f", filepath.Join(*fRepoRoot, "examples", "operator.yaml"))
	err = try.Eventually(func() error {
		out, err := kubectl.Get("deploy", "etcd-operator", "-o=jsonpath='{.status.readyReplicas}'")
		if err != nil {
			return err
		}
		if out != "'1'" {
			return errors.New("expected at least 1 replica of the operator to be available, got: " + out)
		}
		return nil
	}, time.Minute, time.Second*5)
	require.NoError(t, err)

	t.Log("Running tests")
	runAllTests(t, kubectl)
}

func TestE2E_CurrentContext(t *testing.T) {
	if !*fUseCurrentContext {
		t.Skip()
	}

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	configPath := filepath.Join(home, ".kube", "config")
	if path, found := os.LookupEnv("KUBECONFIG"); found {
		configPath = path
	}

	kubectl := kubectlContext{
		t:          t,
		configPath: configPath,
	}

	// TODO run tests call
	_ = kubectl
}

// Starts a Kind cluster on the local machine, exposing port 2379 accepting ETCD connections.
func setupLocalCluster(t *testing.T) (*cluster.Context, func()) {
	t.Log("Starting Kind cluster")
	kind := cluster.NewContext("etcd-e2e")

	err := kind.Create(create.WithV1Alpha3(&kindv1alpha3.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: "kind.sigs.k8s.io/v1alpha3",
		},
		Nodes: []kindv1alpha3.Node{
			{
				Role: "control-plane",
				ExtraPortMappings: []cri.PortMapping{
					{
						ContainerPort: 32379,
						HostPort:      2379,
					},
				},
			},
		},
	}))
	require.NoError(t, err)

	tearDown := func() {
		if !*fCleanup {
			return
		}
		t.Log("Stopping Kind cluster")
		err := kind.Delete()
		assert.NoError(t, err, "failed to stop Kind cluster")
	}

	return kind, tearDown
}

func runAllTests(t *testing.T, kubectl *kubectlContext) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	// Deploy the cluster custom resources.
	kubectl.Apply("-f", filepath.Join(*fRepoRoot, "examples", "cluster.yaml"))

	etcdClient, err := etcd.New(etcdConfig)
	require.NoError(t, err)

	err = try.Eventually(func() error {
		t.Log("Checking if ETCD is available")
		members, err := etcd.NewMembersAPI(etcdClient).List(ctx)
		if err != nil {
			return err
		}

		if len(members) != expectedClusterSize {
			return errors.New(fmt.Sprintf("expected %d etcd peers, got %d", expectedClusterSize, len(members)))
		}
		return nil
	}, time.Minute*2, time.Second*10)
	require.NoError(t, err)
	t.Log("ETCD is reachable from host machine")
}

type kubectlContext struct {
	t          *testing.T
	configPath string
}

func (k *kubectlContext) do(args ...string) ([]byte, error) {
	k.t.Log("Running kubectl " + strings.Join(args, " "))
	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(cmd.Env, "KUBECONFIG="+k.configPath)
	return cmd.CombinedOutput()
}

func (k *kubectlContext) Apply(args ...string) {
	out, err := k.do(append([]string{"apply"}, args...)...)
	require.NoError(k.t, err, string(out))
	k.t.Log(string(out))
}

func (k *kubectlContext) Get(args ...string) (string, error) {
	out, err := k.do(append([]string{"get"}, args...)...)
	return string(out), err
}