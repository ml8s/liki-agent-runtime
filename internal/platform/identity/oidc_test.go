package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOIDCSourceMapsVerifiedClaims(t *testing.T) {
	key := testRSAKey(t)
	issuer := testIssuer(t, key, testJWKS(t, key))
	source, err := NewOIDCSource(testContext(t), OIDCConfig{
		Issuer:        issuer.URL,
		Audience:      "agent-runtime",
		UsernameClaim: "preferred_username",
		TenantClaim:   "tenant",
	})
	if err != nil {
		t.Fatalf("NewOIDCSource() error = %v", err)
	}
	now := time.Now()
	raw := testJWT(t, key, map[string]any{
		"iss":                issuer.URL,
		"aud":                "agent-runtime",
		"sub":                "auth|123",
		"preferred_username": "alice",
		"tenant":             "tenant-a",
		"exp":                now.Add(time.Hour).Unix(),
		"iat":                now.Add(-time.Minute).Unix(),
	})
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	identity, ok := source.IdentityFromRequest(request)
	if !ok || identity.UserID != "alice" || identity.Tenant != "tenant-a" || identity.MutualTLS {
		t.Fatalf("identity = %+v ok=%v, want alice/tenant-a", identity, ok)
	}
	if !source.AuthenticatesCaller() {
		t.Fatal("OIDC source does not authenticate caller")
	}
}

func TestOIDCSourceAcceptsCaseInsensitiveBearerScheme(t *testing.T) {
	key := testRSAKey(t)
	issuer := testIssuer(t, key, testJWKS(t, key))
	source, err := NewOIDCSource(testContext(t), OIDCConfig{Issuer: issuer.URL, Audience: "agent-runtime"})
	if err != nil {
		t.Fatalf("NewOIDCSource() error = %v", err)
	}
	raw := testJWT(t, key, map[string]any{
		"iss": issuer.URL, "aud": "agent-runtime", "sub": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	request.Header.Set("Authorization", "bEaReR "+raw)
	identity, ok := source.IdentityFromRequest(request)
	if !ok || identity.UserID != "alice" {
		t.Fatalf("identity = %+v ok=%v, want alice via case-insensitive scheme", identity, ok)
	}
}

func TestOIDCSourceRejectsInvalidCredentials(t *testing.T) {
	key := testRSAKey(t)
	issuer := testIssuer(t, key, testJWKS(t, key))
	source, err := NewOIDCSource(testContext(t), OIDCConfig{
		Issuer:   issuer.URL,
		Audience: "agent-runtime",
	})
	if err != nil {
		t.Fatalf("NewOIDCSource() error = %v", err)
	}

	missing := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	if _, ok := source.IdentityFromRequest(missing); ok {
		t.Fatal("request without bearer token was accepted")
	}
	invalid := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	invalid.Header.Set("Authorization", "Bearer invalid")
	if _, ok := source.IdentityFromRequest(invalid); ok {
		t.Fatal("invalid token was accepted")
	}
	wrongMethod := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	wrongMethod.Header.Set("Authorization", "Basic alice")
	if _, ok := source.IdentityFromRequest(wrongMethod); ok {
		t.Fatal("non-bearer credential was accepted")
	}
	if _, ok := source.IdentityFromRequest(nil); ok {
		t.Fatal("nil request was accepted")
	}

	expired := testJWT(t, key, map[string]any{
		"iss": issuer.URL, "aud": "agent-runtime", "sub": "alice",
		"exp": time.Now().Add(-time.Hour).Unix(), "iat": time.Now().Add(-2 * time.Hour).Unix(),
	})
	expiredRequest := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	expiredRequest.Header.Set("Authorization", "Bearer "+expired)
	if _, ok := source.IdentityFromRequest(expiredRequest); ok {
		t.Fatal("expired token was accepted")
	}
}

func TestOIDCSourceRejectsInvalidClaims(t *testing.T) {
	key := testRSAKey(t)
	issuer := testIssuer(t, key, testJWKS(t, key))
	source, err := NewOIDCSource(testContext(t), OIDCConfig{
		Issuer: issuer.URL, Audience: "agent-runtime", UsernameClaim: "login", TenantClaim: "tid",
	})
	if err != nil {
		t.Fatalf("NewOIDCSource() error = %v", err)
	}
	raw := testJWT(t, key, map[string]any{
		"iss": issuer.URL, "aud": "agent-runtime", "sub": "subject", "login": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	request := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	identity, ok := source.IdentityFromRequest(request)
	if !ok || identity.UserID != "alice" || identity.Tenant != "" {
		t.Fatalf("identity = %+v ok=%v, want alice", identity, ok)
	}

	invalid := testJWT(t, key, map[string]any{
		"iss": issuer.URL, "aud": "agent-runtime", "login": 42,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	invalidRequest := httptest.NewRequest(http.MethodPost, "/ag-ui", nil)
	invalidRequest.Header.Set("Authorization", "Bearer "+invalid)
	if _, ok := source.IdentityFromRequest(invalidRequest); ok {
		t.Fatal("non-string identity claim was accepted")
	}
}

func TestOIDCSourceRequiresConfiguration(t *testing.T) {
	if _, err := NewOIDCSource(testContext(t), OIDCConfig{Issuer: "", Audience: "aud"}); err == nil {
		t.Fatal("missing issuer was accepted")
	}
	issuer := testIssuer(t, testRSAKey(t), testJWKS(t, testRSAKey(t)))
	if _, err := NewOIDCSource(testContext(t), OIDCConfig{Issuer: issuer.URL, Audience: ""}); err == nil {
		t.Fatal("missing audience was accepted")
	}
	if strings.Contains(issuer.URL, " ") {
		t.Fatal("invalid test issuer")
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

func testJWKS(t *testing.T, key *rsa.PrivateKey) map[string]any {
	t.Helper()
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(bigEndianUint(key.E)),
	}}}
}

func bigEndianUint(value int) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, uint64(value))
	for index, byteValue := range out {
		if byteValue != 0 {
			return out[index:]
		}
	}
	return out[len(out)-1:]
}

func testIssuer(t *testing.T, key *rsa.PrivateKey, jwks map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"issuer":                                server.URL,
			"jwks_uri":                              server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, jwks)
	})
	_ = key
	return server
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}

func testJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal JWT claims: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}
