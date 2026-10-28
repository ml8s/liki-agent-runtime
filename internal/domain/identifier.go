package domain

import "unicode"

// ID is a logical cross-service identifier. The agent does not own foreign
// tables; IDs are opaque audit correlation values.
type ID string

// MaxIdentifierLength bounds untrusted identifiers before they reach ADK,
// audit storage, tracing, and in-memory lifecycle maps.
const MaxIdentifierLength = 128

// ValidIdentifier accepts bounded printable identifiers while preserving
// provider-specific identifier alphabets. It is the single definition shared by
// the runtime and the transport boundary.
func ValidIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifierLength {
		return false
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return false
		}
	}
	return true
}

// AttemptID uniquely identifies one protocol-driven execution attempt. For
// externally driven protocols (A2A) it doubles as the audit run identity; the
// distinct type keeps it from being confused with protocol RunIDs at compile
// time.
type AttemptID string

// Valid reports whether the attempt identifier is bounded and printable.
func (id AttemptID) Valid() bool {
	return ValidIdentifier(string(id))
}
