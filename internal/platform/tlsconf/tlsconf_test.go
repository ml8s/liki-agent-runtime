package tlsconf_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/platform/tlsconf"
)

type testPKI struct {
	caFile         string
	serverCertFile string
	serverKeyFile  string
	caPool         *x509.CertPool
	serverCert     tls.Certificate
	clientCert     tls.Certificate
}

// newTestPKI generates an ephemeral CA plus a server leaf (127.0.0.1 SAN) and a
// client leaf signed by the same CA.
func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "liki-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA to pool")
	}

	writeLeaf := func(name, uri string, ipAddresses []net.IP, serial int64) (string, string, tls.Certificate) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate %s key: %v", name, err)
		}
		parsedURI, err := url.Parse(uri)
		if err != nil {
			t.Fatalf("parse URI: %v", err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			URIs:         []*url.URL{parsedURI},
			IPAddresses:  ipAddresses,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create %s certificate: %v", name, err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal %s key: %v", name, err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		certFile := filepath.Join(dir, name+".crt")
		keyFile := filepath.Join(dir, name+".key")
		if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
			t.Fatalf("write %s certificate: %v", name, err)
		}
		if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
			t.Fatalf("write %s key: %v", name, err)
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("load %s key pair: %v", name, err)
		}
		return certFile, keyFile, pair
	}

	serverCertFile, serverKeyFile, serverPair := writeLeaf(
		"server", "spiffe://liki/runtime/test", []net.IP{net.ParseIP("127.0.0.1")}, 2)
	_, _, clientPair := writeLeaf("web", "spiffe://liki/web", nil, 3)

	return testPKI{
		caFile:         caFile,
		serverCertFile: serverCertFile,
		serverKeyFile:  serverKeyFile,
		caPool:         pool,
		serverCert:     serverPair,
		clientCert:     clientPair,
	}
}

func TestServerConfigRequiresAndVerifiesClientCertificate(t *testing.T) {
	pki := newTestPKI(t)
	config, err := tlsconf.ServerConfig(pki.serverCertFile, pki.serverKeyFile, pki.caFile)
	if err != nil {
		t.Fatalf("ServerConfig() error = %v", err)
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", config.ClientAuth)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = config
	server.StartTLS()
	defer server.Close()

	anonymous := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pki.caPool},
	}}
	if _, err := anonymous.Get(server.URL); err == nil {
		t.Fatal("client without a certificate must be rejected")
	}

	authorized := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:      pki.caPool,
			Certificates: []tls.Certificate{pki.clientCert},
		},
	}}
	response, err := authorized.Get(server.URL)
	if err != nil {
		t.Fatalf("client with a CA-signed certificate rejected: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

func TestServerConfigRejectsUnusableInputs(t *testing.T) {
	dir := t.TempDir()
	if _, err := tlsconf.ServerConfig(
		filepath.Join(dir, "missing.crt"),
		filepath.Join(dir, "missing.key"),
		filepath.Join(dir, "missing-ca.pem"),
	); err == nil {
		t.Fatal("missing files must fail")
	}

	pki := newTestPKI(t)
	emptyCA := filepath.Join(dir, "empty-ca.pem")
	if err := os.WriteFile(emptyCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	if _, err := tlsconf.ServerConfig(pki.serverCertFile, pki.serverKeyFile, emptyCA); err == nil {
		t.Fatal("a CA file without certificates must fail")
	}
}
