package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadsServe(t *testing.T) {
	dir := t.TempDir()
	bin := []byte("companion")
	_ = os.WriteFile(filepath.Join(dir, "nebula-linux-amd64"), bin, 0755)
	sum := sha256.Sum256(bin)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	d := &downloads{dir: dir, digests: map[string]string{}}
	get := func(platform, have string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/update/"+platform, nil)
		if have != "" {
			r.Header.Set("If-None-Match", have)
		}
		w := httptest.NewRecorder()
		d.serve(w, r, platform)
		return w
	}
	if w := get("linux-amd64", `"old"`); w.Code != 200 || w.Header().Get("ETag") != etag || w.Body.String() != "companion" {
		t.Fatalf("download: %d %q", w.Code, w.Header().Get("ETag"))
	}
	if w := get("linux-amd64", etag); w.Code != http.StatusNotModified {
		t.Fatalf("up to date: %d", w.Code)
	}
	for _, p := range []string{"windows-amd64", "../etc", "linux-amd64/.."} {
		if w := get(p, ""); w.Code != 404 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}
