package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/external-dns/provider/google"
	"sigs.k8s.io/external-dns/provider/webhook"
)

func TestDnsGoogle(t *testing.T) {
	ctx := context.Background()

	cfgs := os.Getenv("CFG")

	edns := &ExternalDNSProvider{
		GoogleProject: "dmeshgate",
	}
	json.Unmarshal([]byte(cfgs), edns)

	gdns, err := google.NewGoogleDNSClient(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Provider with no explicit zone
	zp, err := google.NewGoogleZoneProvider(ctx, gdns, edns.GoogleProject, "", "")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zp.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Zones:")
	zn := ""
	for _, ep := range z {
		fmt.Println(ep.Name, ep.DnsName, ep.Description, ep.Labels , ep)
		zn = ep.Name
	}

	zp1, err := google.NewGoogleZoneProvider(ctx, gdns, edns.GoogleProject, z[zn].Name, z[zn].DnsName)
	zp1.Records(ctx)

	// Try the HTTP-based provider

	err = RunGCPDNSProvider(context.Background(), edns, ":8081")
	if err != nil {
		t.Fatal(err)
	}

	wp, err := webhook.NewWebhookProvider("http://localhost:8081")
	if err != nil {
		t.Fatalf("Failed to create webhook provider: %v", err)
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
