package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	eTLS "gitlab.com/go-extension/tls"
	"go.uber.org/zap/zaptest"
)

// genCertPEM returns a fresh self-signed certificate and key in PEM form.
func genCertPEM(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// atomicWrite writes b to a temp file in the same directory and renames it over p.
func atomicWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.Name(), p); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func leafDER[T tls.Certificate | eTLS.Certificate](c *cert[T]) []byte {
	switch v := any(c.get()).(type) {
	case *tls.Certificate:
		return v.Certificate[0]
	case *eTLS.Certificate:
		return v.Certificate[0]
	}
	return nil
}

func pemDER(t *testing.T, b []byte) []byte {
	t.Helper()
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatal("invalid pem")
	}
	return blk.Bytes
}

func waitLeaf[T tls.Certificate | eTLS.Certificate](t *testing.T, c *cert[T], want []byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bytes.Equal(leafDER(c), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("certificate was not reloaded in time")
}

func closeCert[T tls.Certificate | eTLS.Certificate](t *testing.T, c *cert[T]) {
	t.Helper()
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not return")
	}
	select {
	case <-c.done:
	default:
		t.Fatal("watcher goroutine did not exit")
	}
	// Close must be idempotent.
	c.Close()
}

func testWatchCert[T tls.Certificate | eTLS.Certificate](t *testing.T, load func(string, string) (T, error), splitDirs bool) {
	root := t.TempDir()
	certFile := filepath.Join(root, "cert.pem")
	keyFile := filepath.Join(root, "key.pem")
	if splitDirs {
		if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
			t.Fatal(err)
		}
		keyFile = filepath.Join(root, "private", "key.pem")
	}
	c1, k1 := genCertPEM(t, "one.test")
	writeFile(t, certFile, c1)
	writeFile(t, keyFile, k1)

	c, err := tryCreateWatchCert(certFile, keyFile, load, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCert(t, c)
	if !bytes.Equal(leafDER(c), pemDER(t, c1)) {
		t.Fatal("unexpected initial certificate")
	}

	// (a) atomic replacement via temp file + rename.
	c2, k2 := genCertPEM(t, "two.test")
	atomicWrite(t, keyFile, k2)
	atomicWrite(t, certFile, c2)
	waitLeaf(t, c, pemDER(t, c2), 3*time.Second)

	// (b) a second replacement proves the watch survived the first rename.
	c3, k3 := genCertPEM(t, "three.test")
	atomicWrite(t, keyFile, k3)
	atomicWrite(t, certFile, c3)
	waitLeaf(t, c, pemDER(t, c3), 3*time.Second)

	// (c) a broken PEM must not clear the current certificate.
	atomicWrite(t, certFile, []byte("-----BEGIN CERTIFICATE-----\nbroken\n"))
	time.Sleep(1500 * time.Millisecond)
	if !bytes.Equal(leafDER(c), pemDER(t, c3)) {
		t.Fatal("broken certificate replaced the current one")
	}

	// In-place writes (truncate + write) are picked up too.
	c4, k4 := genCertPEM(t, "four.test")
	writeFile(t, keyFile, k4)
	writeFile(t, certFile, c4)
	waitLeaf(t, c, pemDER(t, c4), 3*time.Second)
}

func TestWatchCertStdTLS(t *testing.T) {
	testWatchCert(t, tls.LoadX509KeyPair, false)
}

func TestWatchCertETLS(t *testing.T) {
	testWatchCert(t, eTLS.LoadX509KeyPair, false)
}

func TestWatchCertSplitDirs(t *testing.T) {
	testWatchCert(t, tls.LoadX509KeyPair, true)
}

func TestWatchCertRemoveThenCreate(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	c1, k1 := genCertPEM(t, "one.test")
	writeFile(t, certFile, c1)
	writeFile(t, keyFile, k1)
	c, err := tryCreateWatchCert(certFile, keyFile, tls.LoadX509KeyPair, zaptest.NewLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeCert(t, c)

	// A missing file at reload time is skipped without clearing the cert.
	if err := os.Remove(certFile); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if !bytes.Equal(leafDER(c), pemDER(t, c1)) {
		t.Fatal("certificate changed while the file was missing")
	}

	c2, k2 := genCertPEM(t, "two.test")
	writeFile(t, keyFile, k2)
	writeFile(t, certFile, c2)
	waitLeaf(t, c, pemDER(t, c2), 3*time.Second)
}

func TestWatchCertMissingDir(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	c1, k1 := genCertPEM(t, "one.test")
	writeFile(t, certFile, c1)
	writeFile(t, keyFile, k1)
	// The loader ignores its arguments so that loading succeeds and the
	// w.Add failure path on the missing directory is exercised.
	load := func(string, string) (tls.Certificate, error) { return tls.LoadX509KeyPair(certFile, keyFile) }
	missing := filepath.Join(dir, "nope", "cert.pem")
	if _, err := tryCreateWatchCert(missing, missing, load, nil); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}
