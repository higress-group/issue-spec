// Package auth contains server-side identity and credential primitives. It is
// deliberately independent from the CLI credential resolver in internal/auth.
package auth

import "errors"

var (
	ErrInvalidCredential = errors.New("auth: invalid credential")
	ErrExpiredCredential = errors.New("auth: expired credential")
	ErrRevokedCredential = errors.New("auth: revoked credential")
	ErrDisabledAccount   = errors.New("auth: account disabled")
	ErrInsufficientScope = errors.New("auth: insufficient scope")
	ErrInvalidState      = errors.New("auth: invalid or consumed oauth state")
	ErrInvalidCSRF       = errors.New("auth: invalid csrf token")
	ErrInvalidOrigin     = errors.New("auth: invalid origin")
	ErrConflict          = errors.New("auth: conflict")
	ErrNotFound          = errors.New("auth: not found")
)
