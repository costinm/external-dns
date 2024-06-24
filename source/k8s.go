/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package source

import (
	"context"

	"sigs.k8s.io/external-dns/endpoint"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type K8SSource struct {
	client        kubernetes.Interface
	podInformer   coreinformers.PodInformer
	nodeInformer  coreinformers.NodeInformer
	compatibility string

	Internal string
	K8SSourceConfig
}

// K8SSourceConfig is used to configure a new K8SSource, which creates DNS entries
// for all Nodes, Pods, Services and objects in one cluster.
type K8SSourceConfig struct {
	podInformer   coreinformers.PodInformer
	nodeInformer  coreinformers.NodeInformer

}

// NewK8SSource creates a new source that syncs up all pods to an internal zone, using podname.NAMESPACE.SUFFIX as the DNS name.
// TODO: This will create TXT, SRV  and PTR records as well.
func NewK8SSource(p ClientGenerator, config *Config) (*K8SSource, error) {
	kubeClient, err := p.KubeClient()
	if err != nil {
		return nil, err
	}
	ps := &K8SSource{
		client:        kubeClient,
	}
	return ps, ps.Init(context.Background())
}

func (ps *K8SSource) Init(ctx context.Context) error {
	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(ps.client, 0, kubeinformers.WithNamespace(""))

	podInformer := informerFactory.Core().V1().Pods()
	nodeInformer := informerFactory.Core().V1().Nodes()

	podInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod := obj.(*corev1.Pod)
				e := endpoint.NewEndpoint(pod.Name+"."+pod.Namespace, "A", pod.Status.PodIP)
				log.Println("Added", e)
			},
			UpdateFunc: func(old, obj interface{}) {
				pod := obj.(*corev1.Pod)
				e := endpoint.NewEndpoint(pod.Name+"."+pod.Namespace, "A", pod.Status.PodIP)
				log.Println("Updated", e)
			},
			DeleteFunc: func(obj interface{}) {
				pod := obj.(*corev1.Pod)
				e := endpoint.NewEndpoint(pod.Name+"."+pod.Namespace, "A", pod.Status.PodIP)
				log.Println("Delete", e)
			},
		},
	)
	nodeInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
			},
		},
	)
	ps.podInformer = podInformer
	ps.nodeInformer = nodeInformer

	informerFactory.Start(ctx.Done())

	// wait for the local cache to be populated.
	if err := waitForCacheSync(context.Background(), informerFactory); err != nil {
		return err
	}

	return nil
}

// AddEventHandler is supposed to trigger a full resync. This is not supported and
// should not be implemented - too much data and overhead. Instead, this source
// should send incremental updates. See Istio ServiceEntry source as well.
func (*K8SSource) AddEventHandler(ctx context.Context, handler func()) {
}

func (ps *K8SSource) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {
	pods, err := ps.podInformer.Lister().Pods("").List(labels.Everything())
	if err != nil {
		return nil, err
	}

	endpointMap := make(map[endpoint.EndpointKey][]string)
	for _, pod := range pods {
		if pod.Spec.HostNetwork {
			log.Debugf("skipping pod %s. hostNetwork", pod.Name)
			continue
		}
		if pod.Status.PodIP != "" {
			// return internal endpoint IPs
			addToEndpointMap(endpointMap, pod.Name+"."+pod.Namespace+".p."+ps.Internal, "A", pod.Status.PodIP)
		}
	}
	endpoints := []*endpoint.Endpoint{}
	for key, targets := range endpointMap {
		endpoints = append(endpoints, endpoint.NewEndpoint(key.DNSName, key.RecordType, targets...))
	}
	return endpoints, nil
}

