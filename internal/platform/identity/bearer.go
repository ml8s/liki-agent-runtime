package identity

import "strings"

// BearerToken extracts the credential from an Authorization header using the
// case-insensitive Bearer scheme required by RFC 7235. It is the single
// parsing rule shared by service-token, ops-token, and OIDC verification.
func BearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
