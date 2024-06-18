/*
Copyright 2020 The Kubernetes Authors.

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
	"fmt"
	"sort"
	"strings"
	"text/template"

	log "github.com/sirupsen/logrus"
	networkingv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	istioclient "istio.io/client-go/pkg/clientset/versioned"
	istioinformers "istio.io/client-go/pkg/informers/externalversions"
	networkingv1alpha3informer "istio.io/client-go/pkg/informers/externalversions/networking/v1alpha3"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"sigs.k8s.io/external-dns/endpoint"
)

// virtualServiceSource is an implementation of Source for Istio VirtualService objects.
// The implementation uses the spec.hosts values for the hostnames.
// Use targetAnnotationKey to explicitly set Endpoint.
type ServiceEntrySource struct {
	kubeClient               kubernetes.Interface
	istioClient              istioclient.Interface
	namespace                string
	annotationFilter         string
	fqdnTemplate             *template.Template
	combineFQDNAnnotation    bool
	ignoreHostnameAnnotation bool

	virtualserviceInformer   networkingv1alpha3informer.ServiceEntryInformer
}

func NewIstioServiceEntrySource(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	istioClient istioclient.Interface,
	namespace string,
	annotationFilter string,
	fqdnTemplate string,
	combineFQDNAnnotation bool,
	ignoreHostnameAnnotation bool,
) (Source, error) {
	tmpl, err := parseTemplate(fqdnTemplate)
	if err != nil {
		return nil, err
	}

	// Use shared informers to listen for add/update/delete of services/pods/nodes in the specified namespace.
	// Set resync period to 0, to prevent processing when nothing has changed
	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(kubeClient, 0, kubeinformers.WithNamespace(namespace))

	istioInformerFactory := istioinformers.NewSharedInformerFactoryWithOptions(istioClient, 0, istioinformers.WithNamespace(namespace))
	virtualServiceInformer := istioInformerFactory.Networking().V1alpha3().ServiceEntries()

	// Add default resource event handlers to properly initialize informer.

	virtualServiceInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				log.Debug("service entry added")
			},
		},
	)

	informerFactory.Start(ctx.Done())
	istioInformerFactory.Start(ctx.Done())

	// wait for the local cache to be populated.
	if err := waitForCacheSync(context.Background(), informerFactory); err != nil {
		return nil, err
	}
	if err := waitForCacheSync(context.Background(), istioInformerFactory); err != nil {
		return nil, err
	}

	return &ServiceEntrySource{
		kubeClient:               kubeClient,
		istioClient:              istioClient,
		namespace:                namespace,
		annotationFilter:         annotationFilter,
		fqdnTemplate:             tmpl,
		combineFQDNAnnotation:    combineFQDNAnnotation,
		ignoreHostnameAnnotation: ignoreHostnameAnnotation,
		virtualserviceInformer:   virtualServiceInformer,
	}, nil
}

// Endpoints returns endpoint objects for each host-target combination that should be processed.
// Retrieves all VirtualService resources in the source's namespace(s).
func (sc *ServiceEntrySource) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {
	virtualServices, err := sc.virtualserviceInformer.Lister().ServiceEntries(sc.namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	//virtualServices, err = sc.filterByAnnotations(virtualServices)
	//if err != nil {
	//	return nil, err
	//}

	var endpoints []*endpoint.Endpoint

	for _, virtualService := range virtualServices {
		// Check controller annotation to see if we are responsible.
		controller, ok := virtualService.Annotations[controllerAnnotationKey]
		if ok && controller != controllerAnnotationValue {
			log.Debugf("Skipping VirtualService %s/%s because controller value does not match, found: %s, required: %s",
				virtualService.Namespace, virtualService.Name, controller, controllerAnnotationValue)
			continue
		}

		gwEndpoints, err := sc.dnsRecordsFromServiceEntry(ctx, virtualService)
		if err != nil {
			return nil, err
		}

		// apply template if host is missing on VirtualService
		//if (sc.combineFQDNAnnotation || len(gwEndpoints) == 0) && sc.fqdnTemplate != nil {
		//	iEndpoints, err := sc.endpointsFromTemplate(ctx, virtualService)
		//	if err != nil {
		//		return nil, err
		//	}
		//
		//	if sc.combineFQDNAnnotation {
		//		gwEndpoints = append(gwEndpoints, iEndpoints...)
		//	} else {
		//		gwEndpoints = iEndpoints
		//	}
		//}

		if len(gwEndpoints) == 0 {
			log.Debugf("No endpoints could be generated from VirtualService %s/%s", virtualService.Namespace, virtualService.Name)
			continue
		}

		log.Debugf("Endpoints generated from VirtualService: %s/%s: %v", virtualService.Namespace, virtualService.Name, gwEndpoints)
		endpoints = append(endpoints, gwEndpoints...)
	}

	for _, ep := range endpoints {
		sort.Sort(ep.Targets)
	}

	return endpoints, nil
}

// AddEventHandler adds an event handler that should be triggered if the watched Istio VirtualService changes.
func (sc *ServiceEntrySource) AddEventHandler(ctx context.Context, handler func()) {
	log.Debug("Adding event handler for Istio ServiceEntry")

	sc.virtualserviceInformer.Informer().AddEventHandler(eventHandlerFunc(handler))
}

// dnsRecordsFromServiceEntry extracts the endpoints from an Istio VirtualService Config object
func (sc *ServiceEntrySource) dnsRecordsFromServiceEntry(ctx context.Context, se *networkingv1alpha3.ServiceEntry) ([]*endpoint.Endpoint, error) {
	var endpoints []*endpoint.Endpoint
	//var err error

	resource := fmt.Sprintf("virtualservice/%s/%s", se.Namespace, se.Name)

	ttl := getTTLFromAnnotations(se.Annotations, resource)


	providerSpecific, setIdentifier := getProviderSpecificAnnotations(se.Annotations)

	for _, host := range se.Spec.Hosts {
		if host == "" || host == "*" {
			continue
		}

		parts := strings.Split(host, "/")

		// If the input hostname is of the form my-namespace/foo.bar.com, remove the namespace
		// before appending it to the list of endpoints to create
		if len(parts) == 2 {
			host = parts[1]
		}
		targets := endpoint.Targets{}
		for _, sea := range se.Spec.Addresses {
			targets = append(targets, sea)
		}

		// Auto-allocation should take into account the info in DNS - and set an annotation.

		if len( targets) > 0 {
			endpoints = append(endpoints, endpointsForHostname(host, targets, ttl, providerSpecific, setIdentifier, resource)...)
		}
	}

	return endpoints, nil
}


