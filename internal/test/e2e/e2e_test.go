package e2e

import (
	"flag"
	"fmt"
	etcdv1alpha1 "github.com/improbable-eng/etcd-cluster-operator/api/v1alpha1"
	"github.com/improbable-eng/etcd-cluster-operator/controllers"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/exec"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
	"time"
)

var (
	fUseKind = flag.Bool("kind", false, "Creates a Kind cluster to run the tests against")
	fUseCurrentContext = flag.Bool("current-context", false, "Runs the tests against the current kubernetes context, the path to kube config defaults to ~/.kube/config, unless overridden by the KUBECONFIG environment variable")
	fRepoRoot = flag.String("repo-root", "", "The absolute path to the root of the etcd-cluster-operator git repository.")
)

func TestE2E_Kind(t *testing.T) {
	if !*fUseKind {
		t.Skip()
	}

	tag := fmt.Sprint(time.Now().Nanosecond())

	k8sConfig, _, err := setupLocalCluster(tag)
	require.NoError(t, err, "failed to set up the local Kind cluster")

	err = etcdv1alpha1.AddToScheme(scheme.Scheme)
	require.NoError(t, err)
	k8sClient, err := client.New(k8sConfig, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	runAllTests(t, k8sClient)
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

	k8sConfig, err := clientcmd.BuildConfigFromFlags("", configPath)
	require.NoError(t, err)

	err = etcdv1alpha1.AddToScheme(scheme.Scheme)
	require.NoError(t, err)
	k8sClient, err := client.New(k8sConfig, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	runAllTests(t, k8sClient)
}

func runAllTests(t *testing.T, k8sClient client.Client) {
	kubectl(t, "apply", "--wait", "--kustomize", filepath.Join(*fRepoRoot, "config", "crd"))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme.Scheme,
		Port:               9443,
	})
	require.NoError(t, err)

	err = (&controllers.EtcdPeerReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("EtcdPeer"),
	}).SetupWithManager(mgr)
	require.NoError(t, err)

	// Add new resources here.
	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		err := mgr.Start(stopCh)
		require.NoError(t, err)
	}()

	kubectl(t, "apply", "-f",  filepath.Join(*fRepoRoot, "examples", "cluster.yaml"))

	time.Sleep(time.Minute)
}

func kubectl(t *testing.T, args ...string) {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "received error: %s", string(out))
}
