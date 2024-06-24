package endpoint

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DNSServiceSepc represents an external dns service.
//
type DNSServiceSpec struct {
	// Protocol used to communicate with the provider - one of the build
	// in implementations "aws", "azure", "gcp", "rfc2136", "route53",
	// "alidns", "cloudflare", "dnsimple", "dnsmadeeasy", "infoblox",
	// "linode", "namedotcom", "ovh", "rfc2136", "ultradns"...
	Protocol string

	// URL to the provider's API endpoint, if not hardcoded by the protocol.
	// This will be the Webhook address for out-of-tree providers.
	Address string

	Zones map[string]string
}

type DNSZone struct {
	Name string
	Domain string

}

type DNSSource struct {
	Name string
	Domain string

}

type DNSServiceStatus struct {
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DNSEndpoint is a contract that a user-specified CRD must implement to be used as a source for external-dns.
// The user-specified CRD should also have the status sub-resource.
// +k8s:openapi-gen=true
// +groupName=externaldns.k8s.io
// +kubebuilder:resource:path=dnsendpoints
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=https://github.com/kubernetes-sigs/external-dns/pull/2007"
// +versionName=v1alpha1

type DNSServiceProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSServiceSpec   `json:"spec,omitempty"`
	Status DNSServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// DNSEndpointList is a list of DNSEndpoint objects
type DNSServiceProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSServiceProvider `json:"items"`
}
