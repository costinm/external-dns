package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/google"
	webhookapi "sigs.k8s.io/external-dns/provider/webhook/api"
)

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

	p *google.GoogleProvider
}


func RunGCPDNSProvider(ctx context.Context, cfg *ExternalDNSProvider, addr string) error {
	var domainFilter endpoint.DomainFilter
	//if cfg.RegexDomainFilter.String() != "" {
	//	domainFilter = endpoint.NewRegexDomainFilter(cfg.RegexDomainFilter, cfg.RegexDomainExclusion)
	//} else {
	domainFilter = endpoint.NewDomainFilterWithExclusions(cfg.DomainFilter, cfg.ExcludeDomains)
	//}
	zoneIDFilter := provider.NewZoneIDFilter(cfg.ZoneIDFilter)

	// TODO: private zone support
	p, err := google.NewGoogleProvider(ctx, cfg.GoogleProject, domainFilter, zoneIDFilter, 1000, time.Second, "", false)
	if err != nil {
		return err
	}
	cfg.p = p

	ch := make(chan struct{})
	go webhookapi.StartHTTPApi(p, ch, cfg.WebhookProviderReadTimeout, cfg.WebhookProviderWriteTimeout, addr)
  <- ch
	return nil
}

func main() {
	cfgs := os.Getenv("CFG")
	edns := &ExternalDNSProvider{
		GoogleProject: "dmeshgate",
	}
	json.Unmarshal([]byte(cfgs), edns)

	RunGCPDNSProvider(context.Background(), edns, ":8080")

	log.Println("Started DNS provider for Google Cloud DNS.")
	select{}
}
