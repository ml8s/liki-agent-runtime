package identity

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/url"
	"testing"
)

func TestCertificateSourceMapsVerifiedCertificateSubject(t *testing.T) {
	source := NewCertificateSource()

	uri, err := url.Parse("spiffe://cluster/ns/agent/sa/caller")
	if err != nil {
		t.Fatal(err)
	}
	uriRequest := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			URIs:           []*url.URL{uri},
			EmailAddresses: []string{"caller@example.com"},
			Subject:        pkix.Name{CommonName: "ignored", Organization: []string{"tenant-a"}},
		}}}
		request.Header.Set(TLSUserHeader, "spoofed")
	})
	identity, ok := source.IdentityFromRequest(uriRequest)
	if !ok || identity.UserID != uri.String() || identity.Tenant != "tenant-a" || !identity.MutualTLS {
		t.Fatalf("URI identity = %+v ok=%v, want certificate subject with tenant", identity, ok)
	}

	emailRequest := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			EmailAddresses: []string{"caller@example.com"},
			Subject:        pkix.Name{CommonName: "ignored"},
		}}}
	})
	identity, ok = source.IdentityFromRequest(emailRequest)
	if !ok || identity.UserID != "caller@example.com" {
		t.Fatalf("email identity = %+v ok=%v, want certificate email", identity, ok)
	}

	commonNameRequest := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: "machine-client"},
		}}}
	})
	identity, ok = source.IdentityFromRequest(commonNameRequest)
	if !ok || identity.UserID != "machine-client" {
		t.Fatalf("common-name identity = %+v ok=%v, want certificate common name", identity, ok)
	}
}

func TestCertificateSourceRejectsUnusableSubjects(t *testing.T) {
	source := NewCertificateSource()

	emptyCertificate := httptestRequest(t, func(request *http.Request) {
		request.TLS = tlsState()
	})
	if _, ok := source.IdentityFromRequest(emptyCertificate); ok {
		t.Fatal("certificate without subject was accepted")
	}

	invalidTenant := httptestRequest(t, func(request *http.Request) {
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: "caller", Organization: []string{"bad\x00tenant"}},
		}}}
	})
	if _, ok := source.IdentityFromRequest(invalidTenant); ok {
		t.Fatal("certificate with invalid organization was accepted")
	}

	plain := httptestRequest(t, func(request *http.Request) {
		request.Header.Set(TLSUserHeader, "alice")
	})
	if _, ok := source.IdentityFromRequest(plain); ok {
		t.Fatal("identity was accepted without a verified peer certificate")
	}
	if _, ok := source.IdentityFromRequest(nil); ok {
		t.Fatal("nil request was accepted")
	}
}
