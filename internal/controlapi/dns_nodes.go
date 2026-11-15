package controlapi

import (
	"net/url"
	"strings"

	"github.com/pmkol/mosdns-x/internal/control"
)

func (h *Handler) dnsNodes() []control.DNSNode {
	if h.opts.IdentitySync == nil || h.opts.IdentitySync.Nodes == nil {
		return nil
	}
	return h.opts.IdentitySync.Nodes()
}

type namedDohURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Use the same token path construction for the local and advertised endpoints.
func credentialDohURL(base *url.URL, token string) string {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + url.PathEscape(token)
	return u.String()
}

func (h *Handler) issuedCredential(v control.IssuedCredential) issuedResponse {
	response := issuedResponse{IssuedCredential: v, DNSURL: h.dohURL(v.Token)}
	for _, node := range h.dnsNodes() {
		base, _ := url.Parse(node.PublicDNSURL) // configuration or snapshot validation owns this boundary
		response.DohURLs = append(response.DohURLs, namedDohURL{Name: node.Name, URL: credentialDohURL(base, v.Token)})
	}
	return response
}

type credentialView struct {
	control.Credential
	TokenAvailable bool          `json:"token_available"`
	DNSURL         string        `json:"doh_url,omitempty"`
	DohURLs        []namedDohURL `json:"doh_urls,omitempty"`
}

func (h *Handler) credentialView(c control.Credential, token string) credentialView {
	view := credentialView{Credential: c, TokenAvailable: token != ""}
	if token != "" {
		issued := h.issuedCredential(control.IssuedCredential{Credential: c, Token: token})
		view.DNSURL = issued.DNSURL
		view.DohURLs = issued.DohURLs
	}
	return view
}
