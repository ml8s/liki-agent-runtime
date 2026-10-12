// Package domain holds the runtime's transport-neutral validation invariants and
// stable error codes. It must not import transport, provider, or storage
// packages.
package domain

import "regexp"

// sha256Digest matches the lower-case SHA-256 digest grammar used by
// deployment and audit provenance identifiers.
var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// IsValidSHA256Digest reports whether value is a canonical lower-case
// sha256:<64-hex> identifier.
func IsValidSHA256Digest(value string) bool {
	return sha256Digest.MatchString(value)
}
