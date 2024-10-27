/*
Copyright 2023 The Kubernetes Authors.

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

package remote

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

// Copy of the official external dns provider, to adapt providers using the external-dns endpoint API
// Changes:
// - init mux instead of listen
// - logrus -> slog

const (
	MediaTypeFormatAndVersion = "application/external.dns.webhook+json;version=1"
	ContentTypeHeader         = "Content-Type"
)

type WebhookServer struct {
	Provider provider.Provider
}

func (p *WebhookServer) RecordsHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		records, err := p.Provider.Records(context.Background())
		if err != nil {
			slog.Error("Failed to get Records", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set(ContentTypeHeader, MediaTypeFormatAndVersion)
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(records); err != nil {
			slog.Error("Failed to encode records:", "err", err)
		}
		return
	case http.MethodPost:
		var changes plan.Changes
		if err := json.NewDecoder(req.Body).Decode(&changes); err != nil {
			slog.Error("Failed to decode changes", "err", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := p.Provider.ApplyChanges(context.Background(), &changes)
		if err != nil {
			slog.Error("FailedToApplyChanges", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (p *WebhookServer) AdjustEndpointsHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	pve := []*endpoint.Endpoint{}
	if err := json.NewDecoder(req.Body).Decode(&pve); err != nil {
		slog.Error("FailedToDecode", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set(ContentTypeHeader, MediaTypeFormatAndVersion)
	pve, err := p.Provider.AdjustEndpoints(pve)
	if err != nil {
		slog.Error("FailedToAdjust", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
	if err := json.NewEncoder(w).Encode(&pve); err != nil {
		slog.Error("FailedToEncode", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// NegotiateHandler returns the domain filter for the supported provider.
func (p *WebhookServer) NegotiateHandler(w http.ResponseWriter, req *http.Request) {
	w.Header().Set(ContentTypeHeader, MediaTypeFormatAndVersion)
	json.NewEncoder(w).Encode(p.Provider.GetDomainFilter())
}

// InitHandlers will initialize the HTTP handlers for the given provider.
// Caller can start a server and handle TLS, auth, etc.
// The prefix allows multiple providers to be served on the same port and optional
// parameters like zone.
func InitHandlers(provider provider.Provider, m *http.ServeMux, prefix string) {
	p := WebhookServer{
		Provider: provider,
	}

	// This actually returns the domain filter for the provider - i.e. list of domains.
	// May be extended to return other properties of the provider that can be used to
	// customize the controller. For example, it may return the URL for sending the updates, indication on how to get tokens, etc.
	//
	// This can also be expressed as a CRD.
	m.HandleFunc(prefix + "/", p.NegotiateHandler)

	//
	m.HandleFunc(prefix +"/records", p.RecordsHandler)
	m.HandleFunc(prefix +"/adjustendpoints", p.AdjustEndpointsHandler)
}
