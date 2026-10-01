package benchgate

import (
	"errors"
	"fmt"
)

// Error codes. summary_rules §3 suggests starting with five and adding more as
// needed; these three are the ones a benchmark gate can actually produce.
// ECONFLICT and EUNAUTHORIZED have no meaning here — there is no concurrent
// writer and no caller identity — and a code that is never returned is a line
// claiming to be in use while it is not (§15).
const (
	// EINTERNAL is the code for a failure with no better classification. It is
	// what ErrorCode reports for any error that is not an *Error.
	EINTERNAL = "internal"
	// EINVALID is the code for input the gate cannot act on: a policy whose
	// thresholds are out of range, or two result sets measured on different
	// machines.
	EINVALID = "invalid"
	// ENOTFOUND is the code for a named thing that is absent: a git ref that
	// does not resolve, or a result file that does not exist.
	ENOTFOUND = "not_found"
)

// Error is a leaf error carrying a machine-readable Code and a human-readable
// Message.
//
// Wrapping errors are plain fmt.Errorf("%s: %w", op, err) values rather than a
// second shape of *Error. That produces the same single-line logical stack
// trace summary_rules §3 asks for —
//
//	gate.Check.Run: gotest.Bench: exit status 2
//
// — while staying a form that errors.Is, errors.As, errorlint and wrapcheck all
// already understand. ErrorCode walks whatever chain fmt.Errorf builds.
type Error struct {
	Code    string
	Message string
}

// ErrorCode returns the Code of the innermost *Error in err's chain.
// It returns "" for a nil error and EINTERNAL for an error that carries no
// code, so callers never have to type-assert (summary_rules §3).
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return EINTERNAL
}

// ErrorMessage returns the Message of the innermost *Error in err's chain.
// It returns "" for a nil error, and a generic message for an error that
// carries none, so the message is always safe to show a user.
func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Message
	}
	return "internal error"
}

// Errorf returns a leaf *Error with the given code and a message formatted from
// format and args.
func Errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string { return e.Message }
