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

type PodInternalSource struct {
	client        kubernetes.Interface
	podInformer   coreinformers.PodInformer
	nodeInformer  coreinformers.NodeInformer
	compatibility string

	Internal string
	PodInternalSourceCfg
}

type PodInternalSourceCfg struct {
	podInformer   coreinformers.PodInformer
	nodeInformer  coreinformers.NodeInformer

}

// NewPodInternalSource creates a new source that syncs up all pods to an internal zone, using podname.NAMESPACE.SUFFIX as the DNS name.
// TODO: This will create TXT, SRV  and PTR records as well.
func NewPodInternalSource(ctx context.Context, kubeClient kubernetes.Interface) (*PodInternalSource, error) {
	ps := &PodInternalSource{
		client:        kubeClient,
	}
	return ps, ps.Init(ctx)
}

func (ps *PodInternalSource) Init(ctx context.Context) error {
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

func (*PodInternalSource) AddEventHandler(ctx context.Context, handler func()) {
}

func (ps *PodInternalSource) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {
	pods, err := ps.podInformer.Lister().Pods("").List(labels.Everything())
	if err != nil {
		return nil, err
	}

	endpointMap := make(map[endpoint.EndpointKey][]string)
	for _, pod := range pods {
		if ps.Internal != "" {
			// return internal endpoint IPs
			addToEndpointMap(endpointMap, pod.Name+"."+pod.Namespace+".p."+ps.Internal, "A", pod.Status.PodIP)
			continue
		}

		if !pod.Spec.HostNetwork {
			log.Debugf("skipping pod %s. hostNetwork=false", pod.Name)
			continue
		}

		targets := getTargetsFromTargetAnnotation(pod.Annotations)

		if domainAnnotation, ok := pod.Annotations[internalHostnameAnnotationKey]; ok {
			domainList := splitHostnameAnnotation(domainAnnotation)
			for _, domain := range domainList {
				if len(targets) == 0 {
					addToEndpointMap(endpointMap, domain, suitableType(pod.Status.PodIP), pod.Status.PodIP)
				} else {
					for _, target := range targets {
						addToEndpointMap(endpointMap, domain, suitableType(target), target)
					}
				}
			}
		}

		if domainAnnotation, ok := pod.Annotations[hostnameAnnotationKey]; ok {
			domainList := splitHostnameAnnotation(domainAnnotation)
			for _, domain := range domainList {
				if len(targets) == 0 {
					node, _ := ps.nodeInformer.Lister().Get(pod.Spec.NodeName)
					for _, address := range node.Status.Addresses {
						recordType := suitableType(address.Address)
						// IPv6 addresses are labeled as NodeInternalIP despite being usable externally as well.
						if address.Type == corev1.NodeExternalIP || (address.Type == corev1.NodeInternalIP && recordType == endpoint.RecordTypeAAAA) {
							addToEndpointMap(endpointMap, domain, recordType, address.Address)
						}
					}
				} else {
					for _, target := range targets {
						addToEndpointMap(endpointMap, domain, suitableType(target), target)
					}
				}
			}
		}

		if ps.compatibility == "kops-dns-controller" {
			if domainAnnotation, ok := pod.Annotations[kopsDNSControllerInternalHostnameAnnotationKey]; ok {
				domainList := splitHostnameAnnotation(domainAnnotation)
				for _, domain := range domainList {
					addToEndpointMap(endpointMap, domain, suitableType(pod.Status.PodIP), pod.Status.PodIP)
				}
			}

			if domainAnnotation, ok := pod.Annotations[kopsDNSControllerHostnameAnnotationKey]; ok {
				domainList := splitHostnameAnnotation(domainAnnotation)
				for _, domain := range domainList {
					node, _ := ps.nodeInformer.Lister().Get(pod.Spec.NodeName)
					for _, address := range node.Status.Addresses {
						recordType := suitableType(address.Address)
						// IPv6 addresses are labeled as NodeInternalIP despite being usable externally as well.
						if address.Type == corev1.NodeExternalIP || (address.Type == corev1.NodeInternalIP && recordType == endpoint.RecordTypeAAAA) {
							addToEndpointMap(endpointMap, domain, recordType, address.Address)
						}
					}
				}
			}
		}
	}
	endpoints := []*endpoint.Endpoint{}
	for key, targets := range endpointMap {
		endpoints = append(endpoints, endpoint.NewEndpoint(key.DNSName, key.RecordType, targets...))
	}
	return endpoints, nil
}

