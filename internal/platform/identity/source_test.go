package identity

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
)

func TestHeaderSourceUsesChannelOwnedHeaders(t *testing.T) {
	source := NewHeaderSource(HeaderSourceConfig{})

	plain := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(PlainUserHeader, "alice")
		request.Header.Set(PlainTenantHeader, "tenant-a")
		request.Header.Set(TLSUserHeader, "spoofed")
	})
	identity, ok := source.IdentityFromRequest(plain)
	if !ok || identity.UserID != "alice" || identity.Tenant != "tenant-a" || identity.MutualTLS {
		t.Fatalf("plain identity = %+v ok=%v, want authenticated alice", identity, ok)
	}

	tls := httptestRequest(t, func(request *http.Request) {
		request.TLS = tlsState()
		request.Header.Set(TLSUserHeader, "bob")
		request.Header.Set(PlainUserHeader, "spoofed")
	})
	identity, ok = source.IdentityFromRequest(tls)
	if !ok || identity.UserID != "bob" || identity.Tenant != "" || !identity.MutualTLS {
		t.Fatalf("mTLS identity = %+v ok=%v, want authenticated bob", identity, ok)
	}
}

func TestHeaderSourceRejectsInvalidOrSpoofedHeaders(t *testing.T) {
	source := NewHeaderSource(HeaderSourceConfig{})
	plain := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(PlainUserHeader, "alice")
		request.Header.Set(PlainTenantHeader, "bad\x00tenant")
	})
	if _, ok := source.IdentityFromRequest(plain); ok {
		t.Fatal("invalid tenant was accepted")
	}

	spoofed := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(TLSUserHeader, "alice")
	})
	if _, ok := source.IdentityFromRequest(spoofed); ok {
		t.Fatal("mTLS header was accepted without a verified peer certificate")
	}

	if _, ok := source.IdentityFromRequest(nil); ok {
		t.Fatal("nil request was accepted")
	}
}

func httptestRequest(t *testing.T, mutate func(*http.Request)) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/ag-ui", nil)
	if err != nil {
		t.Fatal(err)
	}
	mutate(request)
	return request
}

func tlsState() *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
}
