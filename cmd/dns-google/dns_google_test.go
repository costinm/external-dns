package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/external-dns/provider/webhook"
)

func TestDnsGoogle(t *testing.T) {
	cfgs := os.Getenv("CFG")
	edns := &ExternalDNSProvider{
		GoogleProject: "dmeshgate",
	}
	json.Unmarshal([]byte(cfgs), edns)

	err := RunGCPDNSProvider(context.Background(), edns, ":8081")
	if err != nil {
		t.Fatal(err)
	}

	wp, err := webhook.NewWebhookProvider("http://localhost:8081")
	if err != nil {
		t.Fatalf("Failed to create webhook provider: %v", err)
	}

	ctx := context.Background()

	z, err := edns.p.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Zones:")
	for _, ep := range z {
		fmt.Println(ep.Name, ep.DnsName, ep.Description, ep.Labels , ep)
	}

	fmt.Println("Endpoints:")
	r, err := wp.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range r {
		fmt.Println(ep)
	}
}
