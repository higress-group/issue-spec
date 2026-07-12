// Package admin implements bootstrap and tenant administration lifecycle.
package admin

import "errors"

var (
	ErrNotFound               = errors.New("admin: not found")
	ErrConflict               = errors.New("admin: conflict")
	ErrForbidden              = errors.New("admin: forbidden")
	ErrInvalidInput           = errors.New("admin: invalid input")
	ErrVersionConflict        = errors.New("admin: version conflict")
	ErrBootstrapCompleted     = errors.New("admin: bootstrap already completed")
	ErrInvalidBootstrapSecret = errors.New("admin: invalid bootstrap secret")
	ErrLastOrganizationOwner  = errors.New("admin: cannot remove the last active organization owner")
)
