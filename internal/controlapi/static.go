package controlapi

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// viteManifest is where Vite lists the files it emitted with a content hash
// in their name; only those may be cached without revalidation.
const viteManifest = ".vite/manifest.json"

const panelCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// staticAsset is one panel file held in memory: the embedded files never
// change while the process runs, so they are read and hashed once at start
// and compressed once, on the first request that takes gzip.
type staticAsset struct {
	body         []byte
	etag         string
	contentType  string
	hashed       bool // the name changes whenever the content does
	compressible bool

	gzipOnce sync.Once
	gzipBody []byte // nil when gzip does not pay
	gzipETag string
}

// loadStaticAssets reads every file under assets/ plus index.html. A file
// that cannot be read is left out and answered 404, as before.
func loadStaticAssets(fsys fs.FS) map[string]*staticAsset {
	hashed := hashedAssetNames(fsys)
	assets := make(map[string]*staticAsset)
	add := func(name string) {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return
		}
		sum := sha256.Sum256(body)
		tag := base64.RawURLEncoding.EncodeToString(sum[:12])
		a := &staticAsset{body: body, etag: `"` + tag + `"`, hashed: hashed[name]}
		a.contentType = mime.TypeByExtension(path.Ext(name))
		if a.contentType == "" {
			a.contentType = http.DetectContentType(body)
		}
		a.compressible = compressible(a.contentType)
		assets[name] = a
	}
	add("index.html")
	_ = fs.WalkDir(fsys, "assets", func(name string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			add(name)
		}
		return nil
	})
	return assets
}

// hashedAssetNames returns the files the Vite manifest lists: the chunks,
// their CSS and the assets they import. Without a manifest none counts as
// hashed and every file is revalidated.
func hashedAssetNames(fsys fs.FS) map[string]bool {
	raw, err := fs.ReadFile(fsys, viteManifest)
	if err != nil {
		return nil
	}
	var manifest map[string]struct {
		File   string   `json:"file"`
		CSS    []string `json:"css"`
		Assets []string `json:"assets"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return nil
	}
	names := make(map[string]bool)
	for _, chunk := range manifest {
		for _, name := range append(append([]string{chunk.File}, chunk.CSS...), chunk.Assets...) {
			if strings.HasPrefix(name, "assets/") {
				names[name] = true
			}
		}
	}
	return names
}

// gzipped returns the compressed body and its ETag, compressing on first
// use, or nil when gzip saves less than a tenth.
func (a *staticAsset) gzipped() ([]byte, string) {
	a.gzipOnce.Do(func() {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if _, err := zw.Write(a.body); err == nil && zw.Close() == nil && buf.Len() < len(a.body)*9/10 {
			a.gzipBody = buf.Bytes()
			a.gzipETag = strings.TrimSuffix(a.etag, `"`) + `-gz"`
		}
	})
	return a.gzipBody, a.gzipETag
}

func compressible(contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/javascript" ||
		mediaType == "application/json" || mediaType == "image/svg+xml"
}

// acceptsGzip reports whether the request accepts gzip with a nonzero q.
func acceptsGzip(r *http.Request) bool {
	for _, header := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(header, ",") {
			coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
				continue
			}
			if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
				if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
					return false
				}
			}
			return true
		}
	}
	return false
}

// serve writes a, gzipped when the client takes it. Hashed files are cached
// for a year; index.html and anything unlisted are revalidated by ETag.
func (a *staticAsset) serve(w http.ResponseWriter, r *http.Request, name string) {
	h := w.Header()
	h.Set("Content-Type", a.contentType)
	if a.hashed {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	body, etag := a.body, a.etag
	if a.compressible {
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			if gz, gzETag := a.gzipped(); gz != nil {
				body, etag = gz, gzETag
				h.Set("Content-Encoding", "gzip")
			}
		}
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}
