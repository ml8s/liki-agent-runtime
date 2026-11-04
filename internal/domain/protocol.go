package domain

// Protocol identifies the stable external boundary that produced an execution.
// Audit evidence and lifecycle correlation treat these as contract values, not
// adapter-local strings.
type Protocol string

// Supported protocol identities used by runtime execution provenance.
const (
	ProtocolA2A  Protocol = "a2a"
	ProtocolAGUI Protocol = "ag_ui"
)
