//go:build !ui

package web

import "testing"

func TestHeadlessAssets(t *testing.T) {
	if Assets() != nil {
		t.Fatal("headless Assets() must return nil")
	}
}
