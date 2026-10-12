package controlapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func staticTestHandler(withManifest bool) *Handler {
	js := []byte(strings.Repeat("console.log('panel');\n", 200))
	fsys := fstest.MapFS{
		"index.html":                 {Data: []byte("<!doctype html><title>panel</title>")},
		"assets/index-AbCd1234.js":   {Data: js},
		"assets/font-XyZ98765.woff2": {Data: []byte("wOF2 binary")},
		"assets/unlisted.js":         {Data: js},
	}
	if withManifest {
		fsys[viteManifest] = &fstest.MapFile{Data: []byte(`{"index.html":{"file":"assets/index-AbCd1234.js","css":[],"assets":["assets/font-XyZ98765.woff2"],"isEntry":true}}`)}
	}
	return &Handler{static: loadStaticAssets(fsys)}
}

func getStatic(h *Handler, name string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.staticFile(w, r, name)
	return w
}

func TestStaticAssetsCacheHashedFilesAndRevalidateTheRest(t *testing.T) {
	h := staticTestHandler(true)
	for name, want := range map[string]string{
		"assets/index-AbCd1234.js":   "public, max-age=31536000, immutable",
		"assets/font-XyZ98765.woff2": "public, max-age=31536000, immutable",
		"assets/unlisted.js":         "no-cache",
		"index.html":                 "no-cache",
	} {
		w := getStatic(h, name, nil)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != want || w.Header().Get("ETag") == "" {
			t.Fatalf("%s: code=%d headers=%v", name, w.Code, w.Header())
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: no CSP", name)
		}
		again := getStatic(h, name, http.Header{"If-None-Match": {w.Header().Get("ETag")}})
		if again.Code != http.StatusNotModified {
			t.Fatalf("%s: revalidation code=%d", name, again.Code)
		}
	}
	// Without a manifest nothing is trusted to be hashed.
	if w := getStatic(staticTestHandler(false), "assets/index-AbCd1234.js", nil); w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("no manifest: %v", w.Header())
	}
	if w := getStatic(h, ".vite/manifest.json", nil); w.Code != http.StatusNotFound {
		t.Fatalf("manifest served: %d", w.Code)
	}
}

func TestStaticAssetsNegotiateGzip(t *testing.T) {
	h := staticTestHandler(true)
	name := "assets/index-AbCd1234.js"
	plain := getStatic(h, name, nil)
	gz := getStatic(h, name, http.Header{"Accept-Encoding": {"br, gzip;q=0.8"}})
	refused := getStatic(h, name, http.Header{"Accept-Encoding": {"gzip;q=0"}})
	if plain.Header().Get("Content-Encoding") != "" || refused.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip sent to a client that did not take it")
	}
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("Vary") != "Accept-Encoding" || gz.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Fatalf("gzip headers = %v", gz.Header())
	}
	if gz.Body.Len() >= plain.Body.Len() {
		t.Fatalf("gzip body %d >= plain %d", gz.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := io.ReadAll(zr)
	if !bytes.Equal(decoded, plain.Body.Bytes()) {
		t.Fatal("gzip body differs from the file")
	}
	// Already compressed formats are sent as they are.
	if w := getStatic(h, "assets/font-XyZ98765.woff2", http.Header{"Accept-Encoding": {"gzip"}}); w.Header().Get("Content-Encoding") != "" || w.Header().Get("Vary") != "" {
		t.Fatalf("woff2 headers = %v", w.Header())
	}
}
