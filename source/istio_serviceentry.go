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
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"istio.io/api/networking/v1alpha3"
	networkingv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	istioclient "istio.io/client-go/pkg/clientset/versioned"
	istioinformers "istio.io/client-go/pkg/informers/externalversions"
	networkingv1alpha3informer "istio.io/client-go/pkg/informers/externalversions/networking/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	// Integration with external-dns - implement the source interface.
	"sigs.k8s.io/external-dns/endpoint"
)

// TODO:
// - reverse external dns - create SE from DNS as source of truth
// - policy - may also live in the controller or DNS updater !
// - split mode - generate records.yaml from each K8S cluster or from files,
//   second tool will read the records.yaml plus existing entries and update the DNS
// - it should also work offline, using files (CI/CD mode). Review and apply independently.
// - patch the SE for auto-alloc - or for the DNS IP from DNS ( other cluster or tool auto-allocs)
// - don't auto-alloc for http, https
// - multi-cluster - setup a set of clusters ( kubeconfig or the Istio MC), do reverse update (possibly using a primary config cluster)

// ServiceEntrySource is an implementation of Source for Istio ServiceEntry objects.
//
// It is strongly recommended to only use ServiceEntry as DNS config for mesh internal
// names as well as 'egress'.
//
// This Source DOES NOT require or use the annotation - it provides similar behavior to
// Istio DNS interception, but with the ability to use external DNS.
type ServiceEntrySource struct {
	kubeClient kubernetes.Interface

	istioClient istioclient.Interface
	seInformer  networkingv1alpha3informer.ServiceEntryInformer
	ServiceEntrySourceConfig
	syncHandler *OnAnyChange
}

type ServiceEntrySourceConfig struct {
	// MeshExternalNamespace is the namespace for MESH_EXTERNAL ServiceEntry.
	// Allowing arbitrary untrusted namespaces to define DNS records is a security risk.
	MeshExternalNamespace string

	// MeshInternalDomain is the domain suffix for MESH_INTERNAL ServiceEntry.
	// The entry MUST be in the format NAME.NAMESPACE.MESH_DOMAIN.
	MeshInternalDomain string

	// WIP: EgressGatewayVIP is the IP of the egress gateway. All MESH_EXTERNAL ServiceEntry
	// without an IP will get allocate this VIP. Entries should only go to a private
	// zone, and EgressGateway must also be external (not use the zone).
	EgressGatewayVIP []string

	// HttpVIP is a VIP to be assigned to all MESH_INTERNAL ServiceEntry with HTTP or HTTPS
	// ports and without an explicit IP. This is to allow for a single VIP to be used for
	// all HTTP - without relying on auto-allocation and using different IPs. Istio will
	// generate a listener for the VIP and route based on the Host header.
	HttpVIP string

	UpdateServiceEntry bool
}

func NewIstioServiceEntrySourceConfig(
		ctx context.Context,
		kubeClient kubernetes.Interface,
		istioClient istioclient.Interface,
		config ServiceEntrySourceConfig) (Source, error) {

	ses := &ServiceEntrySource{
		kubeClient:            kubeClient,
		istioClient: istioClient,
		ServiceEntrySourceConfig: config,
		syncHandler: &OnAnyChange{},
	}

	ses.syncHandler.source = ses

	// Use shared informers to listen for add/update/delete of services/pods/nodes in the specified namespace.

	istioInformerFactory := istioinformers.NewSharedInformerFactoryWithOptions(istioClient, 0, istioinformers.WithNamespace(""))
	serviceEntryInformer := istioInformerFactory.Networking().V1alpha3().ServiceEntries()

	ses.seInformer = serviceEntryInformer

	// Add default resource event handlers to properly initialize informer.
	// This is required to avoid missing events during the initial synchronization,
	// and will receive all existing SE objects.

	serviceEntryInformer.Informer().AddEventHandler(ses.syncHandler)
	istioInformerFactory.Start(ctx.Done())

	// wait for the local cache to be populated.
	if err := waitForCacheSync(context.Background(), istioInformerFactory); err != nil {
		return nil, err
	}

	return ses, nil
}

func (sc *ServiceEntrySource) SyncFromProvider(ctx context.Context, ep []*endpoint.Endpoint) error {


	return nil
}

func (sc *ServiceEntrySource) PatchSE(ctx context.Context, ns, name, address string) error {
	se := networkingv1alpha3.ServiceEntry{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"external-dns/patched": "true"},
		},
	}

	seBytes, err := json.Marshal(se)
	if err != nil {
		return err
	}

	sc.istioClient.NetworkingV1alpha3().ServiceEntries(ns).Patch(ctx, name, types.StrategicMergePatchType, seBytes, metav1.PatchOptions{FieldManager: "ext-dns"})
	return nil
}

// Endpoints returns endpoint objects for each host-target combination that should be processed.
// Retrieves all VirtualService resources in the source's namespace(s).
func (sc *ServiceEntrySource) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {

	var endpoints []*endpoint.Endpoint

	// External ServiceEntries

	// If namespace empty - all namespaces are listed.
	serviceEntries, err := sc.seInformer.Lister().ServiceEntries(sc.MeshExternalNamespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, se := range serviceEntries {
		if se.Spec.Location !=  v1alpha3.ServiceEntry_MESH_EXTERNAL {
			continue
		}

		gwEndpoints, err := sc.dnsRecordsFromExtServiceEntry(ctx, se)
		if err != nil {
			return nil, err
		}

		slog.Debug("Endpoints generated from VirtualService", "namespace", se.Namespace, "name", se.Name,"records",  gwEndpoints)
		endpoints = append(endpoints, gwEndpoints...)
	}

	// TODO: label to declare 'frontend' vs 'backend' SE

	// If namespace empty - all namespaces are listed.
	serviceEntriesInt, err := sc.seInformer.Lister().ServiceEntries("").List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, se := range serviceEntriesInt {
		if se.Spec.Location !=  v1alpha3.ServiceEntry_MESH_INTERNAL {
			continue
		}

		gwEndpoints, err := sc.dnsRecordsFromServiceEntry(ctx, se)
		if err != nil {
			return nil, err
		}

		slog.Debug("Endpoints generated from VirtualService", "namespace", se.Namespace, "name", se.Name,"records",  gwEndpoints)
		endpoints = append(endpoints, gwEndpoints...)
	}

	for _, ep := range endpoints {
		sort.Sort(ep.Targets)
	}

	return endpoints, nil
}

// AddEventHandler adds an event handler that should be triggered if the watched
// object changes, resulting in scheduling a full resync, with some throttling.
//
// This is triggered by the '--events' option in external-dns default main, and results
// in faster sync of the DNS. It is called before SyncOnce or Start - but it does add
// a second SyncOnce since all existing objects will trigger the events.
func (sc *ServiceEntrySource) AddEventHandler(ctx context.Context, handler func()) {
	sc.syncHandler.resyncF = handler
}

type OnAnyChange struct {
	resyncF func()
	source *ServiceEntrySource
}

func (fn OnAnyChange) OnAdd(obj interface{}, isInInitialList bool) {
	if isInInitialList {
		return
	}
	if fn.resyncF != nil {
		fn.resyncF()
	}
}

func (fn OnAnyChange) OnUpdate(oldObj, newObj interface{})         {
	if fn.resyncF != nil {
		fn.resyncF()
	}
}

func (fn OnAnyChange) OnDelete(obj interface{})                    {
	if fn.resyncF != nil {
		fn.resyncF()
	}
}

func (sc *ServiceEntrySource) dnsRecordsFromServiceEntry(ctx context.Context, se *networkingv1alpha3.ServiceEntry) ([]*endpoint.Endpoint, error) {

	var endpoints []*endpoint.Endpoint

	resource := fmt.Sprintf("serviceentry/%s/%s", se.Namespace, se.Name)

	ttl := getTTLFromAnnotations(se.Annotations, resource)

	for _, host := range se.Spec.Hosts {
		if host == "" || host == "*" {
			continue
		}

		targets := endpoint.Targets{}
		for _, sea := range se.Spec.Addresses {
			targets = append(targets, sea)
		}

		// Auto-allocation should take into account the info in DNS - and set an annotation.

		if len( targets) > 0 {
			endpoints = append(endpoints, endpointsForHostname(host, targets, ttl, nil, "", resource)...)
		}
	}

	return endpoints, nil
}


func (sc *ServiceEntrySource) dnsRecordsFromExtServiceEntry(ctx context.Context, se *networkingv1alpha3.ServiceEntry) ([]*endpoint.Endpoint, error) {

	var endpoints []*endpoint.Endpoint

	resource := fmt.Sprintf("serviceentry/%s/%s", se.Namespace, se.Name)

	ttl := getTTLFromAnnotations(se.Annotations, resource)

	for _, host := range se.Spec.Hosts {
		if host == "" || host == "*" {
			continue
		}

		targets := endpoint.Targets{}
		for _, sea := range se.Spec.Addresses {
			targets = append(targets, sea)
		}

		if len(targets) == 0 && sc.HttpVIP != "" {
			// Is it http only ?
			isHttp := true
			for _, port := range se.Spec.Ports {
				if port.Protocol != "http" && port.Protocol != "https" {
					isHttp = false
					break
				}
			}
			if isHttp {
				targets = append(targets, sc.HttpVIP)
			}
		}

		// Auto-allocation should take into account the info in DNS - and set an annotation.

		if len( targets) > 0 {
			endpoints = append(endpoints, endpointsForHostname(host, targets, ttl, nil, "", resource)...)
		}
	}

	return endpoints, nil
}

