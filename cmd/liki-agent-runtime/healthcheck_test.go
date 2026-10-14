package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckURLNormalizesWildcardAddress(t *testing.T) {
	tests := map[string]string{
		":8083":          "http://127.0.0.1:8083",
		"127.0.0.1:8083": "http://127.0.0.1:8083",
		"localhost:8083": "http://localhost:8083",
	}
	for address, want := range tests {
		if got := healthcheckURL(address); got != want {
			t.Fatalf("healthcheckURL(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestHealthcheckAcceptsOnlySuccessfulReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("LIKI_AGENT_RUNTIME_ADDR", server.Listener.Addr().String())
	if err := healthcheck(); err != nil {
		t.Fatalf("healthcheck() error = %v", err)
	}
}

func TestHealthcheckRejectsFailedReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("LIKI_AGENT_RUNTIME_ADDR", server.Listener.Addr().String())
	if err := healthcheck(); err == nil {
		t.Fatal("healthcheck() unexpectedly succeeded")
	}
}
