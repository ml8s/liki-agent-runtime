package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalJSON encodes a value as canonical JSON bytes. Go's encoding/json
// orders map keys deterministically, so identical values encode identically.
// It is the single canonicalization policy shared by audit digests and
// structured-run output.
func CanonicalJSON(value any) (json.RawMessage, error) {
	return json.Marshal(value)
}

// CanonicalDigest produces a stable size-aware provenance pointer for JSON-like
// values. It never returns the original payload.
func CanonicalDigest(value any) (string, int64, error) {
	raw, err := CanonicalJSON(value)
	if err != nil {
		return "", 0, fmt.Errorf("canonical encode audit value: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), int64(len(raw)), nil
}
