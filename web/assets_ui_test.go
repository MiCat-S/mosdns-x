//go:build ui

package web

import (
	"io/fs"
	"regexp"
	"testing"
)

func TestUIAssets(t *testing.T) {
	assets := Assets()
	if assets == nil {
		t.Fatal("ui Assets() returned nil")
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	refs := regexp.MustCompile(`(?:src|href)="/([^"?]+)`).FindAllSubmatch(index, -1)
	if len(refs) == 0 {
		t.Fatal("index.html does not reference production assets")
	}
	for _, ref := range refs {
		name := string(ref[1])
		if _, err := fs.Stat(assets, name); err != nil {
			t.Errorf("referenced asset %q is not readable: %v", name, err)
		}
	}
}
