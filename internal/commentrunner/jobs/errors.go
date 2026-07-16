package jobs

import "errors"

// ErrTerminalJobFailure classifies a job-scoped execution failure after the
// failed lifecycle state has already been persisted. Long-running runtimes may
// continue dispatching later jobs when every returned error has this class.
var ErrTerminalJobFailure = errors.New("runner job failure reached a persisted terminal state")

type terminalJobFailureError struct{ cause error }

func (e *terminalJobFailureError) Error() string { return e.cause.Error() }
func (e *terminalJobFailureError) Unwrap() error { return e.cause }
func (e *terminalJobFailureError) Is(target error) bool {
	return target == ErrTerminalJobFailure
}

func terminalJobFailure(cause error) error {
	if cause == nil {
		return nil
	}
	return &terminalJobFailureError{cause: cause}
}

// IsOnlyTerminalJobFailures reports whether err consists exclusively of
// persisted, job-scoped terminal failures. It deliberately rejects a joined
// error containing any infrastructure failure.
func IsOnlyTerminalJobFailures(err error) bool {
	if err == nil {
		return false
	}
	if err == ErrTerminalJobFailure {
		return true
	}
	if _, ok := err.(*terminalJobFailureError); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !IsOnlyTerminalJobFailures(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return IsOnlyTerminalJobFailures(wrapped.Unwrap())
	}
	return false
}
