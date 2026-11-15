package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pmkol/mosdns-x/internal/control"
)

func TestIdentityExportTokenCannotAuthenticatePanel(t *testing.T) {
	f := newFixture(t)
	token := strings.Repeat("x", 32)
	calls := 0
	f.handler.opts.IdentitySync = &IdentitySyncOptions{Info: IdentitySyncInfo{Mode: "master"}, Token: token, Export: func(context.Context) (control.IdentitySnapshot, error) {
		calls++
		return control.IdentitySnapshot{Version: 1, Users: []control.IdentityUser{}, Credentials: []control.IdentityCredential{}}, nil
	}}
	for _, tc := range []struct {
		path, auth string
		status     int
	}{{"/api/v1/identity/snapshot", "", 401}, {"/api/v1/identity/snapshot", token, 401}, {"/api/v1/identity/snapshot", "Bearer wrong", 401}, {"/api/v1/identity/snapshot", "Bearer " + token, 200}, {"/api/v1/session", "Bearer " + token, 401}, {"/api/v1/admin/users", "Bearer " + token, 401}} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s status=%d want=%d", tc.path, w.Code, tc.status)
		}
	}
	if calls != 1 {
		t.Fatalf("export calls=%d", calls)
	}
	f.handler.opts.IdentitySync.Info.Mode = "follower"
	w := req(f.handler, http.MethodGet, "/api/v1/identity/snapshot", "", nil, "")
	if w.Code != 404 {
		t.Fatalf("follower export=%d", w.Code)
	}
}

func TestIdentitySyncSessionMetadataAndSafeHealth(t *testing.T) {
	f := newFixture(t)
	now := f.handler.opts.Now().UTC()
	last := now.Add(-2 * time.Minute)
	f.handler.opts.IdentitySync = &IdentitySyncOptions{Info: IdentitySyncInfo{Mode: "follower", MasterPanelURL: "https://master.example"}, Token: strings.Repeat("secret", 8), Interval: 30 * time.Second, Health: func() IdentitySyncHealth {
		return IdentitySyncHealth{Mode: "follower", Source: "master.example", LastAttemptAt: &now, LastSuccessAt: &last, LastError: "http_503", Users: 2, Credentials: 3}
	}}
	cookie, _ := login(t, f.handler, "alice", "password-for-alice")
	w := req(f.handler, http.MethodGet, "/api/v1/session", "", cookie, "")
	var response sessionResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.IdentitySync == nil || response.IdentitySync.MasterPanelURL != "https://master.example" {
		t.Fatalf("session=%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), f.handler.opts.IdentitySync.Token) {
		t.Fatal("token exposed")
	}
	report := f.handler.healthSnapshot()
	if report.IdentitySync == nil || report.IdentitySync.Source != "master.example" {
		t.Fatal("missing health status")
	}
	metric := report.Metrics[len(report.Metrics)-1]
	if metric.Key != "identity_sync" || metric.Status != "warning" || metric.Reason != "sync_failed" {
		t.Fatalf("metric=%+v", metric)
	}
	f.handler.opts.IdentitySync.Health = func() IdentitySyncHealth {
		return IdentitySyncHealth{Mode: "follower", LastSuccessAt: &now, LastError: "period_mismatch"}
	}
	if m := f.handler.identitySyncMetric(now); m.Status != "warning" || m.Reason != "period_mismatch" {
		t.Fatalf("mismatch=%+v", m)
	}
	f.handler.opts.IdentitySync.Health = func() IdentitySyncHealth { return IdentitySyncHealth{Mode: "follower"} }
	if metric := f.handler.identitySyncMetric(now); metric.Status != "critical" || metric.Reason != "sync_never" {
		t.Fatalf("never=%+v", metric)
	}
}

func TestIdentitySyncConflictCodes(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		err  error
		code string
	}{{control.ErrIdentityManagedByMaster, "identity_managed_by_master"}, {control.ErrIdentityPeriodLocked, "identity_period_locked"}} {
		w := httptest.NewRecorder()
		f.handler.serviceError(w, errors.Join(errors.New("context"), tc.err))
		if w.Code != 409 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("response=%d %s", w.Code, w.Body.String())
		}
	}
}
