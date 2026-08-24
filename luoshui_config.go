package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sitecustomizeMarker = "# HUJU_EXTERNAL_PROXY_LOADER_V1"
const runLoaderMarker = "# HUJU_RUN_PROXY_LOADER_V1"

var sitecustomizeLoader = []byte("\n" + sitecustomizeMarker + "\ntry:\n    import huiju_luoshui_proxy_patch\nexcept Exception:\n    pass\n")
var runLoader = []byte("\n" + runLoaderMarker + "\ntry:\n    import huiju_luoshui_proxy_patch\nexcept Exception:\n    pass\n\n")

//go:embed huiju_luoshui_proxy_patch.py
var luoshuiRuntimePatch []byte

func FindLuoshuiRoot(appDir string) (string, error) {
	dir, err := filepath.Abs(appDir)
	if err != nil {
		return "", err
	}
	for i := 0; i < 6; i++ {
		if info, statErr := os.Stat(filepath.Join(dir, "洛水.exe")); statErr == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("请将外接软件放在洛水目录内或其子目录中；当前目录：%s", appDir)
}

// ConfigureLuoshuiExternalProxy updates only the Luoshui settings used by the
// bridge. A one-time backup is kept beside settings.json before modification.
func ConfigureLuoshuiExternalProxy(root string) (bool, error) {
	patchChanged, err := EnsureLuoshuiRuntimePatch(root)
	if err != nil {
		return false, err
	}
	settingsPath := filepath.Join(root, "data", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("解析洛水 settings.json 失败: %w", err)
	}
	values := map[string]string{
		"apiUrl":                 "http://localhost:5400/v1",
		"gptApiKey":              "local-proxy",
		"geminiApiKey":           "local-proxy",
		"luoshuiRelayApiKey":     "local-proxy",
		"localGrokVideoBaseUrl":  "http://localhost:5400",
		"localGrokVideoApi":      "local-proxy",
		"localGrokImageBaseUrl":  "http://localhost:8000",
		"localGrokImageApi":      "local-proxy",
		"luoshuiNanoBananaApi":   "local-proxy",
		"imageHostApi":           "local-proxy",
		"grokVideoLuoshuiApi":    "local-proxy",
		"localNanoBananaBaseUrl": "http://localhost:8000",
		"localNanoBananaApi":     "local-proxy",
	}
	changed := false
	for key, value := range values {
		if current, ok := settings[key].(string); !ok || current != value {
			settings[key] = value
			changed = true
		}
	}
	if !changed {
		return patchChanged, nil
	}
	backupPath := settingsPath + ".bak_external_proxy"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		if err := os.WriteFile(backupPath, data, 0600); err != nil {
			return false, fmt.Errorf("备份洛水设置失败: %w", err)
		}
	}
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	tmpPath := settingsPath + fmt.Sprintf(".%d.tmp", time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, updated, 0600); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("写入洛水设置失败: %w", err)
	}
	return true, nil
}

func EnsureLuoshuiRuntimePatch(root string) (bool, error) {
	patchPath := filepath.Join(root, "huiju_luoshui_proxy_patch.py")
	currentPatch, readErr := os.ReadFile(patchPath)
	patchChanged := readErr != nil || string(currentPatch) != string(luoshuiRuntimePatch)
	if patchChanged {
		if err := os.WriteFile(patchPath, luoshuiRuntimePatch, 0600); err != nil {
			return false, fmt.Errorf("安装洛水运行时代理失败: %w", err)
		}
	}

	sitecustomizePath := filepath.Join(root, "sitecustomize.py")
	sitecustomize, err := os.ReadFile(sitecustomizePath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("读取 sitecustomize.py 失败: %w", err)
	}
	sitecustomizeChanged := !strings.Contains(string(sitecustomize), sitecustomizeMarker)
	if sitecustomizeChanged {
		if len(sitecustomize) > 0 {
			backupPath := sitecustomizePath + ".bak_before_huiju"
			if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
				if err := os.WriteFile(backupPath, sitecustomize, 0600); err != nil {
					return false, fmt.Errorf("备份 sitecustomize.py 失败: %w", err)
				}
			}
		}
		updated := append(append([]byte(nil), sitecustomize...), sitecustomizeLoader...)
		if err := os.WriteFile(sitecustomizePath, updated, 0600); err != nil {
			return false, fmt.Errorf("启用洛水运行时代理失败: %w", err)
		}
	}
	runChanged, err := ensureLuoshuiRunLoader(root)
	if err != nil {
		return false, err
	}
	return patchChanged || sitecustomizeChanged || runChanged, nil
}

func ensureLuoshuiRunLoader(root string) (bool, error) {
	runPath := filepath.Join(root, "run.py")
	source, err := os.ReadFile(runPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取洛水 run.py 失败: %w", err)
	}
	if strings.Contains(string(source), runLoaderMarker) {
		return false, nil
	}
	text := string(source)
	markers := []string{"# 启动主程序", "# Start main", "try:\n    import main", "try:\r\n    import main"}
	index := -1
	for _, marker := range markers {
		if found := strings.Index(text, marker); found >= 0 {
			index = found
			break
		}
	}
	if index < 0 {
		return false, fmt.Errorf("无法在洛水 run.py 中定位主程序入口，未修改该文件")
	}
	backupPath := runPath + ".bak_before_huiju"
	if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
		if err := os.WriteFile(backupPath, source, 0600); err != nil {
			return false, fmt.Errorf("备份洛水 run.py 失败: %w", err)
		}
	}
	updated := append([]byte(nil), source[:index]...)
	updated = append(updated, runLoader...)
	updated = append(updated, source[index:]...)
	if err := os.WriteFile(runPath, updated, 0600); err != nil {
		return false, fmt.Errorf("安装洛水 run.py 代理加载器失败: %w", err)
	}
	return true, nil
}
