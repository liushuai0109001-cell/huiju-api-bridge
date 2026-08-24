package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultUpdateManifestURL = "https://huiju.v888.art/huiju-api-bridge/latest.json"

type UpdateConfig struct {
	Enabled      bool   `json:"enabled"`
	ManifestURL  string `json:"manifest_url"`
	CheckOnStart bool   `json:"check_on_start"`
}

type UpdateManifest struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Notes       string `json:"notes"`
}

func updateManifestURL(cfg UpdateConfig) string {
	if value := strings.TrimSpace(os.Getenv("HUIJU_UPDATE_MANIFEST_URL")); value != "" {
		return value
	}
	if value := strings.TrimSpace(cfg.ManifestURL); value != "" {
		return value
	}
	return defaultUpdateManifestURL
}

func normalizeVersion(value string) []int {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), "v"))
	parts := strings.Split(value, ".")
	result := make([]int, 3)
	for index := 0; index < len(parts) && index < len(result); index++ {
		text := parts[index]
		for len(text) > 0 && (text[0] < '0' || text[0] > '9') {
			text = text[1:]
		}
		if number, err := strconv.Atoi(text); err == nil {
			result[index] = number
		}
	}
	return result
}

func isNewerVersion(candidate, current string) bool {
	left, right := normalizeVersion(candidate), normalizeVersion(current)
	for index := range left {
		if left[index] != right[index] {
			return left[index] > right[index]
		}
	}
	return false
}

func checkForUpdate(cfg UpdateConfig, client *http.Client) (UpdateManifest, error) {
	if !cfg.Enabled {
		return UpdateManifest{}, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Get(updateManifestURL(cfg))
	if err != nil {
		return UpdateManifest{}, fmt.Errorf("访问更新清单失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return UpdateManifest{}, fmt.Errorf("更新清单返回 HTTP %d", response.StatusCode)
	}
	var manifest UpdateManifest
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		return UpdateManifest{}, fmt.Errorf("解析更新清单失败：%w", err)
	}
	if strings.TrimSpace(manifest.Version) == "" || strings.TrimSpace(manifest.DownloadURL) == "" {
		return UpdateManifest{}, fmt.Errorf("更新清单缺少 version 或 download_url")
	}
	return manifest, nil
}

func openUpdateURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("更新下载地址为空")
	}
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", value).Start()
	}
	return exec.Command("xdg-open", value).Start()
}
