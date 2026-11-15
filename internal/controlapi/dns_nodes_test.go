package controlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pmkol/mosdns-x/internal/control"
)

func TestPersonalAddressesRemainAvailableOnCredentialLists(t *testing.T) {
	f := newFixture(t)
	nodes := []control.DNSNode{{Name: "广州", PublicDNSURL: "https://gz.example/dns-query"}, {Name: "成都", PublicDNSURL: "https://cd.example/custom/doh/"}}
	f.handler.opts.IdentitySync = &IdentitySyncOptions{Info: IdentitySyncInfo{Mode: "master"}, Nodes: func() []control.DNSNode { return nodes }}
	f.handler.opts.SystemInfo = func(context.Context) (SystemInfo, error) { return SystemInfo{}, nil }
	admin, adminCSRF := login(t, f.handler, "admin", "password-for-admin")
	alice, aliceCSRF := login(t, f.handler, "alice", "password-for-alice")
	for _, tc := range []struct {
		name, path string
		cookie     *http.Cookie
		csrf       string
	}{{"self", "/api/v1/me/credentials", alice, aliceCSRF}, {"admin", "/api/v1/admin/users/" + f.user2.ID + "/credentials", admin, adminCSRF}} {
		t.Run(tc.name, func(t *testing.T) {
			w := req(f.handler, http.MethodPost, tc.path, `{"name":"phone"}`, tc.cookie, tc.csrf)
			var issued issuedResponse
			if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
				t.Fatalf("create status=%d", w.Code)
			}
			check := func(local string, urls []namedDohURL, token string) {
				t.Helper()
				if len(urls) != 2 || urls[0].Name != "广州" || urls[0].URL != "https://gz.example/dns-query/"+token || urls[1].URL != "https://cd.example/custom/doh/"+token || local != "http://localhost/dns-query/"+token {
					t.Fatal("personal URLs do not contain the active token")
				}
			}
			check(issued.DNSURL, issued.DohURLs, issued.Token)
			w = req(f.handler, http.MethodGet, tc.path, "", tc.cookie, "")
			var listed control.PageResult[credentialView]
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || !listed.Items[0].TokenAvailable {
				t.Fatalf("list status=%d", w.Code)
			}
			check(listed.Items[0].DNSURL, listed.Items[0].DohURLs, issued.Token)
			if strings.Contains(w.Body.String(), `"token":`) {
				t.Fatal("credential list added a raw token field")
			}
			w = req(f.handler, http.MethodPost, tc.path+"/"+issued.Credential.ID+"/restore-token", `{"token":"wrong"}`, tc.cookie, tc.csrf)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "token_mismatch") {
				t.Fatalf("mismatch status=%d", w.Code)
			}
			if w = req(f.handler, http.MethodGet, "/api/v1/session", "", tc.cookie, ""); w.Code != 200 {
				t.Fatal("token typo invalidated login session")
			}
			body, _ := json.Marshal(map[string]string{"token": issued.DohURLs[1].URL})
			w = req(f.handler, http.MethodPost, tc.path+"/"+issued.Credential.ID+"/restore-token", string(body), tc.cookie, tc.csrf)
			if w.Code != 200 {
				t.Fatalf("restore status=%d", w.Code)
			}
			w = req(f.handler, http.MethodPost, tc.path+"/"+issued.Credential.ID+"/rotate", "{}", tc.cookie, tc.csrf)
			var rotated issuedResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rotated) != nil {
				t.Fatalf("rotate status=%d", w.Code)
			}
			check(rotated.DNSURL, rotated.DohURLs, rotated.Token)
			if rotated.Token == issued.Token {
				t.Fatal("rotation did not change token")
			}
			w = req(f.handler, http.MethodGet, tc.path, "", tc.cookie, "")
			if strings.Contains(w.Body.String(), issued.Token) {
				t.Fatal("rotation retained old personal URL")
			}
		})
	}
	for _, tc := range []struct {
		path   string
		cookie *http.Cookie
	}{{"/api/v1/me", alice}, {"/api/v1/admin/users/" + f.user1.ID, admin}, {"/api/v1/admin/system", admin}} {
		w := req(f.handler, http.MethodGet, tc.path, "", tc.cookie, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), `"dns_nodes"`) {
			t.Fatalf("base addresses must be absent: %s", tc.path)
		}
	}
	unknown := f.handler.credentialView(control.Credential{}, "")
	if unknown.TokenAvailable || unknown.DNSURL != "" || len(unknown.DohURLs) > 0 {
		t.Fatal("unknown token fabricated a personal URL")
	}
	nodes = nil
	known := f.handler.credentialView(control.Credential{}, "example-token")
	if !known.TokenAvailable || known.DNSURL != "http://localhost/dns-query/example-token" || len(known.DohURLs) > 0 {
		t.Fatal("standalone personal URL changed")
	}
}
