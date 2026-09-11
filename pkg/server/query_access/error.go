package query_access

import (
	"errors"
	"net/http"
)

type Kind string

const (
	InvalidCredential Kind = "invalid_credential"
	Forbidden         Kind = "forbidden"
	RateLimited       Kind = "rate_limited"
	QuotaExceeded     Kind = "quota_exceeded"
	Unavailable       Kind = "unavailable"
)

type Error struct {
	Kind  Kind
	Cause error
}

func New(kind Kind) error {
	return &Error{Kind: kind}
}

func Wrap(kind Kind, cause error) error {
	return &Error{Kind: kind, Cause: cause}
}

func (e *Error) Error() string {
	return string(e.Kind)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func KindOf(err error) (Kind, bool) {
	var accessErr *Error
	if !errors.As(err, &accessErr) {
		return "", false
	}
	return accessErr.Kind, true
}

func HTTPStatus(err error) int {
	kind, ok := KindOf(err)
	if !ok {
		return http.StatusInternalServerError
	}
	switch kind {
	case InvalidCredential:
		return http.StatusUnauthorized
	case Forbidden:
		return http.StatusForbidden
	case RateLimited, QuotaExceeded:
		return http.StatusTooManyRequests
	case Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
