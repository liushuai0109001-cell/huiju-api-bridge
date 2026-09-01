package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureLuoshuiExternalProxy(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"apiUrl":"https://api.example.com","gptApiKey":"old","keepMe":true}`)
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.WriteFile(settingsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := ConfigureLuoshuiExternalProxy(root)
	if err != nil || !changed {
		t.Fatalf("configure: changed=%v err=%v", changed, err)
	}
	var settings map[string]interface{}
	data, _ := os.ReadFile(settingsPath)
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["apiUrl"] != "http://localhost:5400/v1" || settings["keepMe"] != true {
		t.Fatalf("unexpected settings: %#v", settings)
	}
	if _, err := os.Stat(settingsPath + ".bak_external_proxy"); err != nil {
		t.Fatal(err)
	}
	changed, err = ConfigureLuoshuiExternalProxy(root)
	if err != nil || changed {
		t.Fatalf("second configure should be unchanged: changed=%v err=%v", changed, err)
	}
}

func TestFindLuoshuiRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "洛水.exe"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(root, "release", "package")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	found, err := FindLuoshuiRoot(appDir)
	if err != nil || found != root {
		t.Fatalf("found=%q root=%q err=%v", found, root, err)
	}
}

func TestDefaultUploadConfig(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.Upload.Enabled || cfg.Upload.URL != defaultUploadURL || cfg.Upload.APIKey != defaultUploadAPIKey {
		t.Fatalf("unexpected default upload config: %#v", cfg.Upload)
	}
	old := Config{}
	normalizeConfig(&old)
	if old.Upload.URL != defaultUploadURL || old.Upload.APIKey != defaultUploadAPIKey {
		t.Fatalf("legacy config was not filled: %#v", old.Upload)
	}
}

func TestUpstreamAndUploadURLsAreLocked(t *testing.T) {
	cfg := Config{
		Profiles: Profiles{
			Chat:  Profile{BaseURL: "https://other.example/chat"},
			Image: Profile{BaseURL: "https://other.example/image"},
			Video: Profile{BaseURL: "https://other.example/video"},
		},
		Upload: UploadConfig{URL: "https://other.example/upload", APIKey: "key"},
	}
	normalizeConfig(&cfg)
	for name, profile := range map[string]Profile{"chat": cfg.Profiles.Chat, "image": cfg.Profiles.Image, "video": cfg.Profiles.Video} {
		if profile.BaseURL != defaultUpstreamURL {
			t.Fatalf("%s base URL = %q", name, profile.BaseURL)
		}
	}
	if cfg.Upload.URL != defaultUploadURL {
		t.Fatalf("upload URL = %q", cfg.Upload.URL)
	}
}

func TestLegacyFollowVideoDurationMigratesToFixed15Seconds(t *testing.T) {
	cfg := Config{Options: RequestOptions{VideoDuration: "follow"}}
	normalizeConfig(&cfg)
	if cfg.Options.VideoDuration != "15" {
		t.Fatalf("video duration = %q", cfg.Options.VideoDuration)
	}
}

func TestEnsureLuoshuiRuntimePatchPreservesExistingSitecustomize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.py"), []byte("import os\n# 启动主程序\nimport main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sitecustomizePath := filepath.Join(root, "sitecustomize.py")
	original := []byte("existing_value = 42\n")
	if err := os.WriteFile(sitecustomizePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureLuoshuiRuntimePatch(root)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	updated, err := os.ReadFile(sitecustomizePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), string(original)) || !strings.Contains(string(updated), sitecustomizeMarker) {
		t.Fatalf("sitecustomize was not preserved and extended: %s", updated)
	}
	backup, err := os.ReadFile(sitecustomizePath + ".bak_before_huiju")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
	if _, err := os.Stat(filepath.Join(root, "huiju_luoshui_proxy_patch.py")); err != nil {
		t.Fatal(err)
	}
	runSource, err := os.ReadFile(filepath.Join(root, "run.py"))
	if err != nil || !strings.Contains(string(runSource), runLoaderMarker) {
		t.Fatalf("run loader missing: %s err=%v", runSource, err)
	}
	if _, err := os.Stat(filepath.Join(root, "run.py.bak_before_huiju")); err != nil {
		t.Fatal(err)
	}
	changed, err = EnsureLuoshuiRuntimePatch(root)
	if err != nil || changed {
		t.Fatalf("second install changed=%v err=%v", changed, err)
	}
}
