package remote

import (
	"context"
	"errors"
	"time"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/cloudflare"
)

// Convert from the yaml-style config to the external-dns provider config.

type ExternalDNSProvider struct {
	WebhookProviderReadTimeout  time.Duration
	WebhookProviderWriteTimeout time.Duration

	// Used to filter allowed domains
	DomainFilter   []string
	ExcludeDomains []string

	InMemoryZones []string

	// Used by CF to filter zones
	ZoneIDFilter  []string

	GoogleProject string
}

type ProviderAdapter struct {
	provider.Provider
}

func (im *ProviderAdapter) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	return im.Provider.Records(ctx)
}


// NewProvider creates a new provider using the config and in-tree
// external-dns implementations
func NewExternalDNSProvider(ctx context.Context, name string, opts map[string]string) (provider.Provider, error) {
	cfg := &ExternalDNSProvider{}

	var domainFilter endpoint.DomainFilter
	//if cfg.RegexDomainFilter.String() != "" {
	//	domainFilter = endpoint.NewRegexDomainFilter(cfg.RegexDomainFilter, cfg.RegexDomainExclusion)
	//} else {
	domainFilter = endpoint.NewDomainFilterWithExclusions(cfg.DomainFilter, cfg.ExcludeDomains)
	//}
	zoneIDFilter := provider.NewZoneIDFilter(cfg.ZoneIDFilter)

	switch name {
	case "cloudflare":
		return cloudflare.NewCloudFlareProvider(domainFilter, zoneIDFilter, false, false, 100)
	}
	return nil, errors.New("Unknown provider " + name)
}
