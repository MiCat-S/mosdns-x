package recursive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/pmkol/mosdns-x/coremain"
	"github.com/pmkol/mosdns-x/pkg/executable_seq"
	"github.com/pmkol/mosdns-x/pkg/query_context"
	"github.com/pmkol/mosdns-x/pkg/server/dns_handler"
)

func TestArgsToOpts(t *testing.T) {
	dir := t.TempDir()
	hints := filepath.Join(dir, "hints")
	anchor := filepath.Join(dir, "anchor")
	os.WriteFile(hints, []byte(". 3600 NS a.example.\na.example. 3600 A 192.0.2.1\n"), 0o644)
	os.WriteFile(anchor, []byte(". 3600 DS 1 13 2 0000000000000000000000000000000000000000000000000000000000000000\n"), 0o644)
	args := &Args{
		RootHints:        hints,
		TrustAnchor:      anchor,
		DisableDNSSEC:    true,
		DisableQNameMin:  true,
		NoIPv6:           true,
		CacheSize:        1024,
		LocalRoot:        true,
		LocalRootSource:  "/nonexistent/root.zone",
		LocalRootFile:    "root.zone",
		OpportunisticDoT: true,
		SoMark:           1,
		BindToDevice:     "eth0",
	}
	opts, err := newOpts(coremain.NewBP("test", PluginType, nil, nil), args)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.RootHints) != 2 || len(opts.TrustAnchors) != 1 {
		t.Fatalf("files not loaded: %d hints, %d anchors", len(opts.RootHints), len(opts.TrustAnchors))
	}
	if !opts.DisableDNSSEC || !opts.DisableQNameMin || !opts.NoIPv6 || opts.NoIPv4 || opts.CacheSize != 1024 ||
		!opts.LocalRoot || opts.LocalRootSource != args.LocalRootSource || opts.LocalRootFile != "root.zone" ||
		!opts.OpportunisticDoT || opts.Dialer == nil || opts.Logger == nil {
		t.Fatalf("unexpected opts %+v", opts)
	}

	if _, err := newOpts(coremain.NewBP("test", PluginType, nil, nil), &Args{RootHints: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing root hints file accepted")
	}
	if _, err := newRecursive(coremain.NewBP("test", PluginType, nil, nil), &Args{NoIPv4: true, NoIPv6: true}); err == nil {
		t.Fatal("disabling both address families accepted")
	}
}

func TestExec(t *testing.T) {
	p, err := newRecursive(coremain.NewBP("test", PluginType, nil, nil), &Args{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeAXFR)
	qCtx := query_context.NewContext(q, nil)
	want := new(dns.Msg)
	next := executable_seq.WrapExecutable(&executable_seq.DummyExecutable{WantR: want})
	if err := p.Exec(context.Background(), qCtx, next); err != nil {
		t.Fatal(err)
	}
	if qCtx.R() != want {
		t.Fatal("next node was not executed")
	}

	qCtx = query_context.NewContext(q, nil)
	if err := p.Exec(context.Background(), qCtx, nil); err != nil {
		t.Fatal(err)
	}
	if qCtx.R() == nil || qCtx.R().Rcode != dns.RcodeNotImplemented || qCtx.R().Id != q.Id {
		t.Fatalf("unexpected response %v", qCtx.R())
	}
	tr := qCtx.ResponseTrace()
	if tr.Source != query_context.ResponseSourceUpstream || tr.SourceID != "test" || tr.UpstreamLabel != "test" {
		t.Fatalf("unexpected response trace %+v", tr)
	}
}

func TestProxyArg(t *testing.T) {
	bp := coremain.NewBP("test", PluginType, nil, nil)
	if _, err := newOpts(bp, &Args{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("proxy rejected: %v", err)
	}
	if _, err := newOpts(bp, &Args{Proxy: "socks5://127.0.0.1:1080", Socks5: "127.0.0.1:1080"}); err == nil {
		t.Fatal("proxy together with socks5 accepted")
	}
}

func TestEntryObservesRecursiveResolution(t *testing.T) {
	for _, capture := range []bool{false, true} {
		for _, closed := range []bool{false, true} {
			t.Run(fmt.Sprintf("details=%t/closed=%t", capture, closed), func(t *testing.T) {
				p, err := newRecursive(coremain.NewBP("recurse", PluginType, nil, nil), &Args{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { p.Close() })
				if closed {
					p.Close()
				}
				var lister coremain.UpstreamLister = p
				upstreams := lister.ListUpstreams()
				if len(upstreams) != 1 || upstreams[0].ID != "recurse/0" || upstreams[0].Label != "recurse" {
					t.Fatalf("active upstreams = %v", upstreams)
				}
				ids := []string{upstreams[0].ID}
				principal := query_context.Principal{UserID: "user", CredentialID: "device"}
				meta := new(query_context.RequestMeta)
				meta.SetPrincipal(principal)
				var attempts []query_context.UpstreamAttempt
				meta.SetUpstreamObserver(func(a query_context.UpstreamAttempt) { attempts = append(attempts, a) })
				var result dns_handler.Result
				h, err := dns_handler.NewEntryHandler(dns_handler.EntryHandlerOpts{
					Entry: p, CaptureQueryDetails: capture,
					Observe: func(r dns_handler.Result) { result = r },
				})
				if err != nil {
					t.Fatal(err)
				}
				// An unsupported transfer returns locally, so this exercises the
				// complete observation path without querying public DNS.
				q := new(dns.Msg).SetQuestion("example.com.", dns.TypeAXFR)
				q.Id = 42
				resp, err := h.ServeDNS(context.Background(), q, meta)
				if err != nil || resp.Id != 42 || q.Id != 42 {
					t.Fatalf("response = %v, query = %v, error = %v", resp, q, err)
				}
				if len(attempts) != 1 || attempts[0].Principal != principal || attempts[0].UpstreamID != ids[0] || attempts[0].Failed != closed {
					t.Fatalf("observed attempts = %+v", attempts)
				}
				if closed {
					if resp.Rcode != dns.RcodeServerFailure || !result.ExecError || result.UpstreamID != "" || attempts[0].Rcode != -1 {
						t.Fatalf("closed result = %+v, response = %v", result, resp)
					}
				} else if resp.Rcode != dns.RcodeNotImplemented || result.UpstreamID != ids[0] || result.UpstreamLabel != "recurse" ||
					result.ResponseSource != query_context.ResponseSourceUpstream || result.ResponseSourceID != "recurse" || attempts[0].Rcode != dns.RcodeNotImplemented {
					t.Fatalf("selected result = %+v, response = %v", result, resp)
				}
				if !capture {
					if result.Trace != nil {
						t.Fatalf("details disabled, trace = %+v", result.Trace)
					}
					return
				}
				if result.UpstreamRequestEDNS != nil || result.UpstreamResponseEDNS != nil || result.Trace == nil || len(result.Trace.Attempts) != 1 {
					t.Fatalf("unexpected exchange details = %+v", result)
				}
				attempt := result.Trace.Attempts[0]
				if attempt.Plugin != "recurse" || attempt.Upstream != "recurse" || !attempt.Done || attempt.Selected == closed {
					t.Fatalf("journal attempt = %+v", attempt)
				}
				if closed {
					if result.UpstreamStageStatus != query_context.UpstreamStageAttemptedNoSelection || attempt.Error == "" {
						t.Fatalf("failed trace = %+v", result)
					}
				} else if result.UpstreamStageStatus != query_context.UpstreamStageIterative || attempt.Rcode != "NOTIMP" || attempt.Error != "" {
					t.Fatalf("selected trace = %+v", result)
				}
			})
		}
	}
}

func TestLabel(t *testing.T) {
	p, err := newRecursive(coremain.NewBP("recurse", PluginType, nil, nil), &Args{Label: "递归解析"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.DisplayName(); got != "递归解析" {
		t.Fatalf("display name = %q", got)
	}
	if got := p.ListUpstreams(); len(got) != 1 || got[0].ID != "recurse/0" || got[0].Label != "递归解析" {
		t.Fatalf("upstreams = %v", got)
	}
	for _, label := range []string{"bad\nlabel", strings.Repeat("x", 65)} {
		if _, err := newRecursive(coremain.NewBP("recurse", PluginType, nil, nil), &Args{Label: label}); err == nil {
			t.Fatalf("label %q accepted", label)
		}
	}
}
