package main

import (
	"context"
	"log"
	"time"

	"sigs.k8s.io/external-dns/controller"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
	"sigs.k8s.io/external-dns/provider/inmemory"
	"sigs.k8s.io/external-dns/provider/webhook"
	"sigs.k8s.io/external-dns/registry"
	"sigs.k8s.io/external-dns/source"
)

type DnsSource struct {
	Address string
}

func main() {
	ctx := context.Background()

	cfg := &DnsSource{

	}

	source.InstrumentationWrapper = nil

	sg := &source.SingletonClientGenerator{
		KubeConfig: "", //   cfg.KubeConfig,
		APIServerURL: "", // cfg.APIServerURL,
		// If update events are enabled, disable timeout.
		RequestTimeout: func() time.Duration {
			//if cfg.UpdateEvents {
			//	return 0
			//}
			//return cfg.RequestTimeout
			return 1 * time.Second
		}(),
	}
	kc, err := sg.KubeClient()
	ic, err := sg.IstioClient()
	src, err := source.NewIstioServiceEntrySource(ctx, kc, ic, "", "", "", false, false)
	if err != nil {
		log.Fatalf("Failed to create webhook provider: %v", err)
	}

	ep, err  := src.Endpoints(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range ep {
		log.Println(e)
	}

	var p provider.Provider
	if cfg.Address == "" {
		p = inmemory.NewInMemoryProvider(inmemory.InMemoryWithLogging())
	} else {
		// Now push the changed endpoints to provider
		wp, err := webhook.NewWebhookProvider("http://localhost:8081")
		if err != nil {
			log.Fatalf("Failed to create webhook provider: %v", err)
		}
		p = wp
	}

	//registry.NewTXTRegistry(p, cfg.TXTPrefix, cfg.TXTSuffix, cfg.TXTOwnerID, cfg.TXTCacheInterval, cfg.TXTWildcardReplacement, cfg.ManagedDNSRecordTypes, cfg.ExcludeDNSRecordTypes, cfg.TXTEncryptEnabled, []byte(cfg.TXTEncryptAESKey))
	r, err := registry.NewNoopRegistry(p)

	r.Records(ctx)

	ctrl := controller.Controller{
		Source:               src,
		Registry:             r,

		// upsert-only - create and update, doesn't delete
		// create-only - doesn't update
		// sync - delete too
		Policy:               plan.Policies["sync"],
		Interval:             10 * time.Second,
		//DomainFilter:         domainFilter,
		//ManagedRecordTypes:   cfg.ManagedDNSRecordTypes,
		//ExcludeRecordTypes:   cfg.ExcludeDNSRecordTypes,
		MinEventSyncInterval: 5 * time.Second,
	}

	if true {
		err := ctrl.RunOnce(ctx)
		if err != nil {
			log.Fatal(err)
		}
	}

	if true {
		// Add RunOnce as the handler function that will be called when ingress/service sources have changed.
		// Note that k8s Informers will perform an initial list operation, which results in the handler
		// function initially being called for every Service/Ingress that exists
		src.AddEventHandler(ctx, func() {
			// This will be called for all existing SE - causing a lot of churn and a sync.
			//log.Println("SE event handler called.")
			ctrl.ScheduleRunOnce(time.Now()) })
	}

	ctrl.ScheduleRunOnce(time.Now())
	ctrl.Run(ctx)

}
