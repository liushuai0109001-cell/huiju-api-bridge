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

const defaultUpdateManifestURL = "https://raw.githubusercontent.com/liushuai0109001-cell/huiju-api-bridge/main/latest.json"

type UpdateConfig struct {
	Enabled      bool   `json:"enabled"`
	ManifestURL  string `json:"manifest_url"`
	CheckOnStart bool   `json:"check_on_start"`
}

type UpdateManifest struct {
	Version      string   `json:"version"`
	DownloadURL  string   `json:"download_url"`
	DownloadURLs []string `json:"download_urls"`
	SHA256       string   `json:"sha256"`
	Notes        string   `json:"notes"`
}

func (m UpdateManifest) downloadCandidates() []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(m.DownloadURLs)+1)
	for _, value := range append([]string{m.DownloadURL}, m.DownloadURLs...) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
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
	if strings.TrimSpace(manifest.Version) == "" || len(manifest.downloadCandidates()) == 0 {
		return UpdateManifest{}, fmt.Errorf("更新清单缺少 version 或 download_url")
	}
	return manifest, nil
}

func selectFastestDownloadURL(manifest UpdateManifest, client *http.Client) (string, error) {
	candidates := manifest.downloadCandidates()
	if len(candidates) == 0 {
		return "", fmt.Errorf("没有可用的更新下载地址")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	type result struct {
		url string
		err error
	}
	results := make(chan result, len(candidates))
	for _, candidate := range candidates {
		go func(value string) {
			request, err := http.NewRequest(http.MethodGet, value, nil)
			if err != nil {
				results <- result{err: err}
				return
			}
			request.Header.Set("Range", "bytes=0-1023")
			response, err := client.Do(request)
			if err != nil {
				results <- result{err: err}
				return
			}
			response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 400 {
				results <- result{err: fmt.Errorf("HTTP %d", response.StatusCode)}
				return
			}
			results <- result{url: value}
		}(candidate)
	}
	var errors []string
	for range candidates {
		item := <-results
		if item.url != "" {
			return item.url, nil
		}
		if item.err != nil {
			errors = append(errors, item.err.Error())
		}
	}
	return candidates[0], fmt.Errorf("所有下载镜像不可用：%s", strings.Join(errors, "; "))
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
