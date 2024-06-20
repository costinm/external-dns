package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/external-dns/provider/google"
)

func TestDnsGoogle(t *testing.T) {
	ctx := context.Background()

	cfgs := os.Getenv("CFG")

	edns := &google.GoogleProviderCfg{}
	json.Unmarshal([]byte(cfgs), edns)

	if edns.Project == "" {
		// my test project
		edns.Project = "dmeshgate"
	}

	// Provider with no explicit zone
	zp, err := google.NewGoogleProvider(ctx, edns, nil, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zp.Zones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Zones:")
	zn := ""
	zd := ""
	for _, ep := range z {
		fmt.Println(ep.Name, ep.DnsName, ep.Description, ep.Labels, ep)
		zn = ep.Name
		zd = ep.DnsName
	}

	edns.Zones = map[string]string{zn: zd}

	zp1, err := google.NewGoogleProvider(ctx, edns, nil, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = zp1.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// // Try the HTTP-based provider

	// err = RunGCPDNSProvider(context.Background(), edns, ":8081")
	// if err != nil {
	// 	t.Fatal(err)
	// }

	// wp, err := webhook.NewWebhookProvider("http://localhost:8081")
	// if err != nil {
	// 	t.Fatalf("Failed to create webhook provider: %v", err)
	// }
	// fmt.Println("Endpoints:")
	// r, err := wp.Records(ctx)
	// if err != nil {
	// 	t.Fatal(err)
	// }
	// for _, ep := range r {
	// 	fmt.Println(ep)
	// }

}
