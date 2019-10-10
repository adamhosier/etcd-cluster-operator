package e2e

import (
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kindv1alpha3 "sigs.k8s.io/kind/pkg/apis/config/v1alpha3"
	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cluster/create"
	"sigs.k8s.io/kind/pkg/container/cri"
)

func setupLocalCluster(tag string) (k8sClient *rest.Config, teardownFunc func() error, err error) {
	log.Info("Starting Kind cluster")
	kind := cluster.NewContext("etcd-e2e")

	err = kind.Create(create.WithV1Alpha3(&kindv1alpha3.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind: "Cluster",
			APIVersion: "kind.sigs.k8s.io/v1alpha3",
		},
		Nodes: []kindv1alpha3.Node{
			{
				Role: "control-plane",
				ExtraPortMappings: []cri.PortMapping{
					{
						ContainerPort: 2379,
						HostPort: 2379,
					},
				},
			},
		},
	}))
	teardownFunc = func() error {
		log.Info("Stopping Kind cluster")
		return kind.Delete()
	}
	if err != nil {
		return nil, teardownFunc, err
	}

	// Create kubernetes client in to the Kind cluster.
	config, err := clientcmd.BuildConfigFromFlags("", kind.KubeConfigPath())
	if err != nil {
		log.WithError(err).Fatal("Failed to create kubernetes client config")
	}

	return config, teardownFunc, nil
}
