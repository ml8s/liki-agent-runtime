package domain

import "errors"

// ErrInvalidInput marks a caller-supplied value that violates a domain
// invariant.
var ErrInvalidInput = errors.New("invalid input")

// Error is the runtime's coded error. Code is a stable externally visible
// identifier, Message is safe to surface, and Cause preserves the underlying
// error for errors.Is and errors.As.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// NewError builds a coded domain error, optionally wrapping a cause.
func NewError(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, Cause: cause}
}
