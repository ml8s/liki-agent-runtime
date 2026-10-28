package identity

import (
	"crypto/x509"
	"net/http"
	"strings"

	"github.com/ml8s/agent-runtime/internal/domain"
)

// CertificateSource derives the runtime identity from the client certificate
// that the mutual-TLS listener already verified. Unlike channel-owned relay
// headers, the certificate is issued to exactly one subject: a peer that
// presents a valid certificate cannot claim another user or tenant by setting
// request headers.
//
// The subject mapping is deterministic: the first URI SAN (stable machine
// identity, for example SPIFFE) is preferred, then the first email SAN, then
// the common name. The certificate organization maps to the tenant when it is
// a valid identifier. A certificate without a usable subject is rejected.
type CertificateSource struct{}

// NewCertificateSource creates the certificate identity adapter.
func NewCertificateSource() CertificateSource {
	return CertificateSource{}
}

// IdentityFromRequest implements RequestSource.
func (CertificateSource) IdentityFromRequest(request *http.Request) (Identity, bool) {
	if request == nil || !MutualTLSVerified(request) || len(request.TLS.PeerCertificates) == 0 {
		return Identity{}, false
	}
	certificate := request.TLS.PeerCertificates[0]
	userID, ok := certificateUserID(certificate)
	if !ok || !domain.ValidIdentifier(userID) {
		return Identity{}, false
	}
	tenant := ""
	if len(certificate.Subject.Organization) > 0 {
		tenant = strings.TrimSpace(certificate.Subject.Organization[0])
		if tenant != "" && !domain.ValidIdentifier(tenant) {
			return Identity{}, false
		}
	}
	return Identity{UserID: userID, Tenant: tenant, MutualTLS: true}, true
}

func certificateUserID(certificate *x509.Certificate) (string, bool) {
	for _, uri := range certificate.URIs {
		if value := strings.TrimSpace(uri.String()); value != "" {
			return value, true
		}
	}
	for _, email := range certificate.EmailAddresses {
		if value := strings.TrimSpace(email); value != "" {
			return value, true
		}
	}
	if commonName := strings.TrimSpace(certificate.Subject.CommonName); commonName != "" {
		return commonName, true
	}
	return "", false
}
