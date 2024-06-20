package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"sigs.k8s.io/external-dns/provider/google"
	webhookapi "sigs.k8s.io/external-dns/provider/webhook/api"
)

// main starts a barebones webhook for the Google provider.
// Primary goal is to validate the minimal configuration and identify the size of the binary vs exernal-dns.
// Initial test shows 31M versus 138M with all providers (23 vs 98 stripped)
func main() {
	cfgs := os.Getenv("CFG")
	edns := &google.GoogleProviderCfg{}
	json.Unmarshal([]byte(cfgs), edns)

	ctx := context.Background()

	p, err := google.NewGoogleProvider(ctx, edns, nil, nil, "", false)
	if err != nil {
		panic(err)
	}

	ch := make(chan struct{})
	go webhookapi.StartHTTPApi(p, ch, 0, 0, "8080")
	<-ch

	log.Println("Started DNS provider for Google Cloud DNS.")
	select {}
}
