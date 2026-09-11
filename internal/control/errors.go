package control

import "errors"

var (
	ErrInvalidCredential = errors.New("invalid credential")
	ErrForbidden         = errors.New("forbidden")
	ErrRateLimited       = errors.New("rate limited")
	ErrQuotaExceeded     = errors.New("quota exceeded")
	ErrUnavailable       = errors.New("service unavailable")
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrInvalidInput      = errors.New("invalid input")
)
