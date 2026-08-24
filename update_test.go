package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionComparison(t *testing.T) {
	if !isNewerVersion("v1.0.11", "v1.0.10") {
		t.Fatal("newer patch version was not detected")
	}
	if isNewerVersion("1.0.10", "v1.0.10") {
		t.Fatal("same version was reported as newer")
	}
	if !isNewerVersion("2.0.0", "1.99.99") {
		t.Fatal("newer major version was not detected")
	}
}

func TestCheckForUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.11","download_url":"https://cdn.example/bridge.zip","sha256":"abc","notes":"修复更新"}`))
	}))
	defer server.Close()
	manifest, err := checkForUpdate(UpdateConfig{Enabled: true, ManifestURL: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.0.11" || !strings.Contains(manifest.DownloadURL, "bridge.zip") {
		t.Fatalf("manifest=%#v", manifest)
	}
}

func TestCheckForUpdateDisabled(t *testing.T) {
	manifest, err := checkForUpdate(UpdateConfig{Enabled: false}, nil)
	if err != nil || manifest.Version != "" {
		t.Fatalf("manifest=%#v err=%v", manifest, err)
	}
}
