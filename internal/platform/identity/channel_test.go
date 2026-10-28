package identity

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"testing"
)

func TestChannelSourceRoutesByVerifiedChannel(t *testing.T) {
	uri, err := url.Parse("spiffe://cluster/ns/agent/sa/direct")
	if err != nil {
		t.Fatal(err)
	}
	source := NewChannelSource(
		NewHeaderSource(HeaderSourceConfig{}),
		NewCertificateSource(),
	)

	plain := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(PlainUserHeader, "relay-user")
	})
	identity, ok := source.IdentityFromRequest(plain)
	if !ok || identity.UserID != "relay-user" || identity.MutualTLS {
		t.Fatalf("plain identity = %+v ok=%v, want relay user", identity, ok)
	}

	direct := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{uri}}}}
		request.Header.Set(TLSUserHeader, "spoofed")
		request.Header.Set(PlainUserHeader, "spoofed")
	})
	identity, ok = source.IdentityFromRequest(direct)
	if !ok || identity.UserID != uri.String() || !identity.MutualTLS {
		t.Fatalf("direct identity = %+v ok=%v, want certificate subject", identity, ok)
	}
}

func TestChannelSourceFailsClosedWithoutChannelSource(t *testing.T) {
	uri, err := url.Parse("spiffe://cluster/ns/agent/sa/direct")
	if err != nil {
		t.Fatal(err)
	}
	headerOnly := NewChannelSource(NewHeaderSource(HeaderSourceConfig{}), nil)
	direct := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{uri}}}}
	})
	if _, ok := headerOnly.IdentityFromRequest(direct); ok {
		t.Fatal("mutual-TLS channel was accepted without a direct identity source")
	}

	certOnly := NewChannelSource(nil, NewCertificateSource())
	plain := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(PlainUserHeader, "relay-user")
	})
	if _, ok := certOnly.IdentityFromRequest(plain); ok {
		t.Fatal("plain channel was accepted without a relay identity source")
	}
	if _, ok := certOnly.IdentityFromRequest(nil); ok {
		t.Fatal("nil request was accepted")
	}
}
