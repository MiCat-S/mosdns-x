package query_access

import (
	"errors"
	"net/http"
	"testing"
)

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		kind Kind
		want int
	}{
		{InvalidCredential, http.StatusUnauthorized},
		{Forbidden, http.StatusForbidden},
		{RateLimited, http.StatusTooManyRequests},
		{QuotaExceeded, http.StatusTooManyRequests},
		{Unavailable, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			secret := errors.New("must-not-be-exposed")
			err := Wrap(tt.kind, secret)
			if got := HTTPStatus(err); got != tt.want {
				t.Fatalf("HTTPStatus() = %d, want %d", got, tt.want)
			}
			if err.Error() != string(tt.kind) {
				t.Fatalf("Error() = %q", err.Error())
			}
			if !errors.Is(err, secret) {
				t.Fatal("wrapped cause is unavailable to programmatic callers")
			}
		})
	}
}
