package records

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gps "github.com/kryuchenko/google-play-scraper"
)

// Code names what went wrong, the same in both interfaces: an MCP tool error
// reads "code: message", and the CLI prints the code in its error object and
// maps it to an exit status. The set is stable.
type Code string

const (
	CodeInvalid      Code = "invalid_argument"      // fix the arguments; the same call fails again
	CodeNotFound     Code = "not_found"             // no such app or page in this storefront
	CodeNotListed    Code = "not_listed"            // the store returned nothing for this id here
	CodeRateLimited  Code = "rate_limited"          // the store is refusing requests for now; wait
	CodeTimeout      Code = "timeout"               // the store did not answer in time
	CodeBusy         Code = "busy"                  // the server is at capacity
	CodeCancelled    Code = "cancelled"             // the caller cancelled
	CodeConfirmation Code = "confirmation_required" // an expensive run needs explicit consent
	CodeStore        Code = "store_error"           // any other failure talking to the store
	CodeInternal     Code = "internal"              // a bug in gpscrape
)

// Retryable reports whether repeating the same call can succeed without any
// change: after a wait for rate_limited, at once for the rest.
func (c Code) Retryable() bool {
	switch c {
	case CodeRateLimited, CodeTimeout, CodeBusy, CodeStore, CodeNotListed:
		return true
	}
	return false
}

// ExitCode is the gpscrape process status for c. 0 is success, 130 an
// interrupt; the rest:
//
//	2 invalid_argument, confirmation_required
//	3 not_found, not_listed
//	4 rate_limited
//	5 timeout, busy
//	1 anything else
func (c Code) ExitCode() int {
	switch c {
	case CodeInvalid, CodeConfirmation:
		return 2
	case CodeNotFound, CodeNotListed:
		return 3
	case CodeRateLimited:
		return 4
	case CodeTimeout, CodeBusy:
		return 5
	case CodeCancelled:
		return 130
	}
	return 1
}

// Error is an error with a code attached by whoever knew it.
type Error struct {
	Code Code
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Errorf returns an error with code c.
func Errorf(c Code, format string, args ...any) error {
	return &Error{Code: c, Err: fmt.Errorf(format, args...)}
}

// Invalid is Errorf with invalid_argument.
func Invalid(format string, args ...any) error {
	return Errorf(CodeInvalid, format, args...)
}

// Classify returns the code for err: the one attached with Error, or one
// inferred from what the library returned.
func Classify(err error) Code {
	var ce *Error
	var se *gps.StatusError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ce):
		return ce.Code
	case errors.Is(err, context.Canceled):
		return CodeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	case errors.Is(err, gps.ErrBlocked), errors.As(err, &se) && se.Code == 429:
		return CodeRateLimited
	case errors.Is(err, gps.ErrNotFound), errors.As(err, &se) && se.Code == 404:
		return CodeNotFound
	case strings.Contains(err.Error(), "no data returned"):
		// The library's unexported empty-frame error: the batch RPC had
		// nothing for this id, which is almost always an unlisted app.
		return CodeNotListed
	}
	return CodeStore
}
