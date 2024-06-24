package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"sigs.k8s.io/external-dns/pkg/apis/externaldns"
	webhookapi "sigs.k8s.io/external-dns/provider/webhook/api"
)

// main starts a barebones webhook for the Google provider.
// Primary goal is to validate the minimal configuration and identify the size of the binary vs exernal-dns.
// Initial test shows 31M versus 138M with all providers (23 vs 98 stripped)

func InitGoogleProvider() {
	cfgs := os.Getenv("CFG")

	edns := &externaldns.Config{}
	json.Unmarshal([]byte(cfgs), edns)

	ctx := context.Background()

	p, err := NewGoogleProvider(ctx, &edns.ProviderConfig, nil, nil,  false)
	if err != nil {
		panic(err)
	}

	ch := make(chan struct{})
	go webhookapi.StartHTTPApi(p, ch, 0, 0, "8080")
	<-ch

}


// TestDnsGoogle is an e2e test that validates the Google Cloud DNS provider implementation
// and the new config model.
func TestDnsGoogle(t *testing.T) {
	ctx := context.Background()

	cfgs := os.Getenv("CFG")

	edns := &externaldns.Config{}
	json.Unmarshal([]byte(cfgs), edns)

	// Provider with no explicit zone
	zp, err := NewGoogleProvider(ctx, &edns.ProviderConfig, nil, nil,  false)
	if err != nil {
		if edns.ProviderConfig.GoogleProject == "" {
			t.Skip("Requires GOOGLE_PROJECT_ID")
		}
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

	edns.ProviderConfig.Zones = map[string]string{zn: zd}

	zp1, err := NewGoogleProvider(ctx, &edns.ProviderConfig, nil, nil,  false)
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
