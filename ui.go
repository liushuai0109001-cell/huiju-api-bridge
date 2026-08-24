package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

type ProfileControls struct {
	Enabled *walk.CheckBox
	BaseURL *walk.LineEdit
	APIKey  *walk.LineEdit
	Model   *walk.ComboBox
}

type OptionControls struct {
	ImageAspect *walk.ComboBox
	VideoAspect *walk.ComboBox
	Duration    *walk.ComboBox
}

type UploadControls struct {
	Enabled *walk.CheckBox
	URL     *walk.LineEdit
	APIKey  *walk.LineEdit
}

type LicenseControls struct {
	MachineCode *walk.LineEdit
	Status      *walk.Label
	Code        *walk.TextEdit
}

type DesktopUI struct {
	window       *walk.MainWindow
	statusLabel  *walk.Label
	startButton  *walk.PushButton
	launchButton *walk.PushButton
	logEdit      *walk.TextEdit
	chat         ProfileControls
	image        ProfileControls
	video        ProfileControls
	upload       UploadControls
	options      OptionControls
	autoConfig   *walk.CheckBox
	licenseUI    LicenseControls
	store        *ConfigStore
	bridge       *Bridge
	license      *LicenseManager
	logBuffer    *LogBuffer
	appDir       string
	stopRefresh  chan struct{}
	updateLabel  *walk.Label
}

var aspectLabels = []string{"跟随洛水", "1:1", "16:9", "9:16", "4:3", "3:4"}
var aspectValues = []string{"follow", "1:1", "16:9", "9:16", "4:3", "3:4"}
var durationLabels = []string{"15 秒（跟随洛水设置）", "30 秒（外置软件优先）"}
var durationValues = []string{"follow", "30"}

func profilePage(title string, controls *ProfileControls, extra ...Widget) TabPage {
	children := []Widget{
		CheckBox{AssignTo: &controls.Enabled, Text: "接管此类请求"},
		Label{Text: "上游 Base URL"},
		LineEdit{AssignTo: &controls.BaseURL, ToolTipText: "例如 https://api.example.com 或 https://api.example.com/v1"},
		Label{Text: "API Key"},
		LineEdit{AssignTo: &controls.APIKey, PasswordMode: true},
		Label{Text: "目标模型（点击底部“获取模型”刷新选项）"},
		ComboBox{AssignTo: &controls.Model, Editable: false, Model: []string{}},
	}
	children = append(children, extra...)
	return TabPage{
		Title:    title,
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 6},
		Children: children,
	}
}

func uploadPage(controls *UploadControls) TabPage {
	return TabPage{
		Title:  "图床上传",
		Layout: VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 6},
		Children: []Widget{
			CheckBox{AssignTo: &controls.Enabled, Text: "接管洛水参考图上传"},
			Label{Text: "图床上传 URL"},
			LineEdit{AssignTo: &controls.URL, ToolTipText: "例如 https://api.example.com/upload"},
			Label{Text: "图床 API Key"},
			LineEdit{AssignTo: &controls.APIKey, PasswordMode: true},
			Label{Text: "洛水将上传到本机 /upload；真实 URL 和 Key 仅由外接软件使用。"},
		},
	}
}

func licensePage(ui *DesktopUI) TabPage {
	return TabPage{
		Title:  "授权管理",
		Layout: VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			Label{Text: "本机机器码"},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					LineEdit{AssignTo: &ui.licenseUI.MachineCode, ReadOnly: true},
					PushButton{Text: "复制机器码", OnClicked: ui.copyMachineCode},
				},
			},
			Label{Text: "授权状态"},
			Label{AssignTo: &ui.licenseUI.Status, Text: "未授权"},
			Label{Text: "授权码"},
			TextEdit{AssignTo: &ui.licenseUI.Code, MinSize: Size{Width: 400, Height: 170}, VScroll: true},
			PushButton{Text: "激活授权", OnClicked: ui.activateLicense},
		},
	}
}

func NewDesktopUI(store *ConfigStore, bridge *Bridge, logs *LogBuffer, appDir string, license *LicenseManager) *DesktopUI {
	return &DesktopUI{store: store, bridge: bridge, logBuffer: logs, appDir: appDir, license: license}
}

func (ui *DesktopUI) Run() error {
	window := MainWindow{
		AssignTo: &ui.window,
		Title:    "荟聚 API 外接软件 " + appVersion,
		MinSize:  Size{Width: 920, Height: 680},
		Size:     Size{Width: 1040, Height: 760},
		Layout:   VBox{Margins: Margins{Left: 16, Top: 14, Right: 16, Bottom: 14}, Spacing: 10},
		Children: []Widget{
			Composite{
				Layout: HBox{},
				Children: []Widget{
					Label{Text: "洛水协议桥接", Font: Font{PointSize: 13, Bold: true}},
					HSpacer{},
					Label{AssignTo: &ui.statusLabel, Text: "服务未启动", TextColor: walk.RGB(170, 45, 35)},
				},
			},
			TabWidget{
				Pages: []TabPage{
					profilePage("语言模型", &ui.chat),
					profilePage("图片模型", &ui.image,
						Label{Text: "图片画面比例"},
						ComboBox{AssignTo: &ui.options.ImageAspect, Editable: false, Model: aspectLabels},
					),
					profilePage("视频模型", &ui.video,
						Label{Text: "视频时长"},
						ComboBox{AssignTo: &ui.options.Duration, Editable: false, Model: durationLabels},
						Label{Text: "选择 30 秒时覆盖洛水时长；选择 15 秒时保留洛水请求值。"},
						Label{Text: "视频画面比例"},
						ComboBox{AssignTo: &ui.options.VideoAspect, Editable: false, Model: aspectLabels},
					),
					uploadPage(&ui.upload),
					licensePage(ui),
					{
						Title:  "运行日志",
						Layout: VBox{},
						Children: []Widget{
							TextEdit{AssignTo: &ui.logEdit, ReadOnly: true, VScroll: true},
						},
					},
				},
			},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{Text: "保存配置", OnClicked: ui.save},
					PushButton{Text: "获取模型", OnClicked: ui.fetchModels},
					PushButton{AssignTo: &ui.startButton, Text: "启动服务", OnClicked: ui.toggleService},
					PushButton{AssignTo: &ui.launchButton, Text: "启动洛水", OnClicked: ui.launchLuoshui},
					CheckBox{AssignTo: &ui.autoConfig, Text: "自动配置洛水外接接口", Checked: true, ToolTipText: "外接软件启动时及启动洛水前自动配置，并保留 settings.json 备份"},
					HSpacer{},
					PushButton{Text: "打开数据目录", OnClicked: ui.openFolder},
					PushButton{Text: "检查更新", OnClicked: ui.checkUpdate},
					Label{AssignTo: &ui.updateLabel, Text: "更新：未检查"},
				},
			},
		},
	}
	if err := window.Create(); err != nil {
		return err
	}
	ui.load()
	ui.configureLuoshuiOnStartup()
	ui.refreshStatus()
	if cfg := ui.store.Get(); cfg.Update.Enabled && cfg.Update.CheckOnStart {
		go ui.checkUpdateAsync(false)
	}
	if ui.license.Check() == nil {
		if err := ui.bridge.Start(); err != nil {
			walk.MsgBox(ui.window, "启动失败", err.Error(), walk.MsgBoxIconError)
		}
	}
	ui.refreshStatus()
	ui.stopRefresh = make(chan struct{})
	go ui.refreshLogs()
	ui.window.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		close(ui.stopRefresh)
		_ = ui.bridge.Stop()
	})
	ui.window.Show()
	ui.window.Run()
	return nil
}

func (ui *DesktopUI) checkUpdate() {
	go ui.checkUpdateAsync(true)
}

func (ui *DesktopUI) checkUpdateAsync(showResult bool) {
	manifest, err := checkForUpdate(ui.store.Get().Update, nil)
	if err != nil {
		ui.window.Synchronize(func() {
			_ = ui.updateLabel.SetText("更新检查失败")
			if showResult {
				walk.MsgBox(ui.window, "检查更新", err.Error(), walk.MsgBoxIconWarning)
			}
		})
		return
	}
	if manifest.Version == "" || !isNewerVersion(manifest.Version, appVersion) {
		ui.window.Synchronize(func() {
			_ = ui.updateLabel.SetText("更新：已是最新版本")
			if showResult {
				walk.MsgBox(ui.window, "检查更新", "当前已经是最新版本。", walk.MsgBoxIconInformation)
			}
		})
		return
	}
	ui.window.Synchronize(func() {
		_ = ui.updateLabel.SetText("发现新版本 " + manifest.Version)
		message := "发现新版本 " + manifest.Version + "。\r\n\r\n" + manifest.Notes + "\r\n\r\n是否打开下载地址？"
		if walk.MsgBox(ui.window, "发现更新", message, walk.MsgBoxIconInformation|walk.MsgBoxYesNo) == walk.DlgCmdYes {
			selectedURL, selectErr := selectFastestDownloadURL(manifest, nil)
			if selectErr != nil {
				_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 更新镜像探测失败，回退主下载地址：" + selectErr.Error() + "\r\n"))
			}
			if openErr := openUpdateURL(selectedURL); openErr != nil {
				walk.MsgBox(ui.window, "打开下载地址失败", openErr.Error(), walk.MsgBoxIconError)
			}
		}
	})
}

func (ui *DesktopUI) refreshLogs() {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ui.stopRefresh:
			return
		case <-ticker.C:
			text := ui.logBuffer.String()
			ui.window.Synchronize(func() {
				if ui.logEdit.Text() != text {
					_ = ui.logEdit.SetText(text)
					ui.logEdit.SetTextSelection(len([]rune(text)), len([]rune(text)))
				}
			})
		}
	}
}

func (ui *DesktopUI) loadProfile(controls ProfileControls, profile Profile) {
	controls.Enabled.SetChecked(profile.Enabled)
	_ = controls.BaseURL.SetText(profile.BaseURL)
	_ = controls.APIKey.SetText(profile.APIKey)
	setModelChoices(controls.Model, nil, profile.Model)
}

func setModelChoices(combo *walk.ComboBox, models []string, selected string) {
	choices := append([]string(nil), models...)
	selected = strings.TrimSpace(selected)
	found := false
	for _, model := range choices {
		if model == selected {
			found = true
			break
		}
	}
	if selected != "" && !found {
		choices = append([]string{selected}, choices...)
	}
	_ = combo.SetModel(choices)
	for index, model := range choices {
		if model == selected {
			_ = combo.SetCurrentIndex(index)
			return
		}
	}
	if len(choices) > 0 {
		_ = combo.SetCurrentIndex(0)
	}
}

func setOption(combo *walk.ComboBox, values []string, selected string) {
	index := 0
	for i, value := range values {
		if value == selected {
			index = i
			break
		}
	}
	_ = combo.SetCurrentIndex(index)
}

func optionValue(combo *walk.ComboBox, values []string) string {
	index := combo.CurrentIndex()
	if index < 0 || index >= len(values) {
		return values[0]
	}
	return values[index]
}

func (ui *DesktopUI) load() {
	cfg := ui.store.Get()
	ui.loadProfile(ui.chat, cfg.Profiles.Chat)
	ui.loadProfile(ui.image, cfg.Profiles.Image)
	ui.loadProfile(ui.video, cfg.Profiles.Video)
	setOption(ui.options.ImageAspect, aspectValues, cfg.Options.ImageAspectRatio)
	setOption(ui.options.VideoAspect, aspectValues, cfg.Options.VideoAspectRatio)
	setOption(ui.options.Duration, durationValues, cfg.Options.VideoDuration)
	ui.upload.Enabled.SetChecked(cfg.Upload.Enabled)
	_ = ui.upload.URL.SetText(cfg.Upload.URL)
	_ = ui.upload.APIKey.SetText(cfg.Upload.APIKey)
	ui.autoConfig.SetChecked(cfg.Luoshui.AutoConfigure)
	_ = ui.licenseUI.MachineCode.SetText(ui.license.MachineCode())
	ui.refreshLicense()
}

func (ui *DesktopUI) refreshLicense() {
	claims, err := ui.license.Current()
	if err != nil {
		_ = ui.licenseUI.Status.SetText("未授权：" + err.Error())
		ui.licenseUI.Status.SetTextColor(walk.RGB(170, 45, 35))
		return
	}
	expires := time.Unix(claims.ExpiresAt, 0).Format("2006-01-02 15:04:05")
	_ = ui.licenseUI.Status.SetText(fmt.Sprintf("已授权给 %s，到期时间 %s", claims.Customer, expires))
	ui.licenseUI.Status.SetTextColor(walk.RGB(18, 128, 70))
}

func (ui *DesktopUI) copyMachineCode() {
	if err := walk.Clipboard().SetText(ui.license.MachineCode()); err != nil {
		walk.MsgBox(ui.window, "复制失败", err.Error(), walk.MsgBoxIconError)
	}
}

func extractLicenseCode(value string) string {
	for _, field := range strings.Fields(value) {
		if strings.HasPrefix(field, "HJ1.") {
			return field
		}
	}
	return strings.TrimSpace(value)
}

func (ui *DesktopUI) activateLicense() {
	code := extractLicenseCode(ui.licenseUI.Code.Text())
	if code == "" {
		walk.MsgBox(ui.window, "激活失败", "请输入授权码。", walk.MsgBoxIconWarning)
		return
	}
	if _, err := ui.license.Activate(code); err != nil {
		walk.MsgBox(ui.window, "激活失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	_ = ui.licenseUI.Code.SetText("")
	ui.refreshLicense()
	if !ui.bridge.Running() {
		if err := ui.bridge.Start(); err != nil {
			walk.MsgBox(ui.window, "服务启动失败", err.Error(), walk.MsgBoxIconError)
		}
	}
	ui.refreshStatus()
	walk.MsgBox(ui.window, "激活成功", "授权已保存，代理服务已启用。", walk.MsgBoxIconInformation)
}

func profileFromControls(controls ProfileControls) (Profile, error) {
	baseURL := strings.TrimSpace(controls.BaseURL.Text())
	if baseURL != "" {
		if err := validateBaseURL(baseURL); err != nil {
			return Profile{}, err
		}
	}
	return Profile{
		Enabled: controls.Enabled.Checked(), BaseURL: baseURL,
		APIKey: strings.TrimSpace(controls.APIKey.Text()), Model: strings.TrimSpace(controls.Model.Text()),
	}, nil
}

func (ui *DesktopUI) configFromForm() (Config, error) {
	cfg := ui.store.Get()
	var err error
	if cfg.Profiles.Chat, err = profileFromControls(ui.chat); err != nil {
		return Config{}, fmt.Errorf("语言模型: %w", err)
	}
	if cfg.Profiles.Image, err = profileFromControls(ui.image); err != nil {
		return Config{}, fmt.Errorf("图片模型: %w", err)
	}
	if cfg.Profiles.Video, err = profileFromControls(ui.video); err != nil {
		return Config{}, fmt.Errorf("视频模型: %w", err)
	}
	cfg.Options = RequestOptions{
		ImageAspectRatio: optionValue(ui.options.ImageAspect, aspectValues),
		VideoAspectRatio: optionValue(ui.options.VideoAspect, aspectValues),
		VideoDuration:    optionValue(ui.options.Duration, durationValues),
	}
	uploadURL := strings.TrimSpace(ui.upload.URL.Text())
	if uploadURL != "" {
		if err := validateBaseURL(uploadURL); err != nil {
			return Config{}, fmt.Errorf("图床上传: %w", err)
		}
	}
	cfg.Upload = UploadConfig{
		Enabled: ui.upload.Enabled.Checked(),
		URL:     uploadURL,
		APIKey:  strings.TrimSpace(ui.upload.APIKey.Text()),
	}
	cfg.Luoshui.AutoConfigure = ui.autoConfig.Checked()
	return cfg, nil
}

func (ui *DesktopUI) save() {
	cfg, err := ui.configFromForm()
	if err != nil {
		walk.MsgBox(ui.window, "配置错误", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	if err := ui.store.Save(cfg); err != nil {
		walk.MsgBox(ui.window, "保存失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	restart := ui.bridge.Running()
	if restart {
		_ = ui.bridge.Stop()
		if err := ui.bridge.Start(); err != nil {
			walk.MsgBox(ui.window, "重启失败", err.Error(), walk.MsgBoxIconError)
		}
	}
	ui.refreshStatus()
	walk.MsgBox(ui.window, "保存完成", "配置已保存并应用。", walk.MsgBoxIconInformation)
}

func (ui *DesktopUI) toggleService() {
	if ui.bridge.Running() {
		if err := ui.bridge.Stop(); err != nil {
			walk.MsgBox(ui.window, "停止失败", err.Error(), walk.MsgBoxIconError)
		}
	} else if err := ui.bridge.Start(); err != nil {
		walk.MsgBox(ui.window, "启动失败", err.Error(), walk.MsgBoxIconError)
	}
	ui.refreshStatus()
}

func (ui *DesktopUI) refreshStatus() {
	if err := ui.license.Check(); err != nil {
		_ = ui.statusLabel.SetText("未授权 · 服务已锁定")
		ui.statusLabel.SetTextColor(walk.RGB(170, 45, 35))
		_ = ui.startButton.SetText("启动服务")
		return
	}
	if ui.bridge.Running() {
		_ = ui.statusLabel.SetText("运行中 · 127.0.0.1:5400 / 8000")
		ui.statusLabel.SetTextColor(walk.RGB(18, 128, 70))
		_ = ui.startButton.SetText("停止服务")
	} else {
		_ = ui.statusLabel.SetText("服务未启动")
		ui.statusLabel.SetTextColor(walk.RGB(170, 45, 35))
		_ = ui.startButton.SetText("启动服务")
	}
}

func (ui *DesktopUI) fetchModels() {
	cfg, err := ui.configFromForm()
	if err != nil {
		walk.MsgBox(ui.window, "配置错误", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	go func() {
		sections := []struct {
			name    string
			profile Profile
			combo   *walk.ComboBox
		}{{"语言模型", cfg.Profiles.Chat, ui.chat.Model}, {"图片模型", cfg.Profiles.Image, ui.image.Model}, {"视频模型", cfg.Profiles.Video, ui.video.Model}}
		var errors []string
		for _, section := range sections {
			models, fetchErr := fetchModels(section.profile, 30*time.Second)
			if fetchErr != nil {
				errors = append(errors, section.name+": "+fetchErr.Error())
				continue
			}
			sort.Strings(models)
			selected := section.profile.Model
			combo := section.combo
			ui.window.Synchronize(func() { setModelChoices(combo, models, selected) })
		}
		ui.window.Synchronize(func() {
			if len(errors) > 0 {
				walk.MsgBox(ui.window, "获取模型完成", "部分模型获取失败：\r\n"+strings.Join(errors, "\r\n"), walk.MsgBoxIconWarning)
			} else {
				walk.MsgBox(ui.window, "获取模型完成", "三个模型下拉选项已刷新。", walk.MsgBoxIconInformation)
			}
		})
	}()
}

func (ui *DesktopUI) configureLuoshuiOnStartup() {
	if ui.autoConfig == nil || !ui.autoConfig.Checked() {
		return
	}
	root, err := FindLuoshuiRoot(ui.appDir)
	if err != nil {
		_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 自动配置洛水失败：" + err.Error() + "\r\n"))
		return
	}
	changed, err := ConfigureLuoshuiExternalProxy(root)
	if err != nil {
		_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 自动配置洛水失败：" + err.Error() + "\r\n"))
		return
	}
	if changed {
		_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 外接软件启动时已自动配置洛水接口\r\n"))
	}
}

func validateLaunchProfile(profile Profile) error {
	if !profile.Enabled {
		return fmt.Errorf("语言模型接管未启用")
	}
	if strings.TrimSpace(profile.BaseURL) == "" {
		return fmt.Errorf("语言模型 Base URL 未配置")
	}
	if strings.TrimSpace(profile.APIKey) == "" {
		return fmt.Errorf("语言模型 API Key 未配置")
	}
	if strings.TrimSpace(profile.Model) == "" || strings.HasPrefix(profile.Model, "your-") {
		return fmt.Errorf("语言模型未选择")
	}
	return nil
}

func (ui *DesktopUI) launchLuoshui() {
	cfg, err := ui.configFromForm()
	if err != nil {
		walk.MsgBox(ui.window, "配置错误", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	if err := validateLaunchProfile(cfg.Profiles.Chat); err != nil {
		walk.MsgBox(ui.window, "无法启动洛水", err.Error(), walk.MsgBoxIconWarning)
		return
	}
	if err := ui.store.Save(cfg); err != nil {
		walk.MsgBox(ui.window, "保存失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	if ui.bridge.Running() {
		_ = ui.bridge.Stop()
	}
	if err := ui.bridge.Start(); err != nil {
		walk.MsgBox(ui.window, "代理服务启动失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	ui.refreshStatus()
	ui.launchButton.SetEnabled(false)
	_ = ui.launchButton.SetText("校验语言密钥...")
	go func(profile Profile) {
		checkErr := probeChatProfile(profile, 120*time.Second)
		ui.window.Synchronize(func() {
			ui.launchButton.SetEnabled(true)
			_ = ui.launchButton.SetText("启动洛水")
			if IsAuthenticationError(checkErr) {
				walk.MsgBox(ui.window, "语言密钥无效", "中转站拒绝了语言模型密钥（HTTP 401）。请在“语言模型”页更新 API Key 后重试。\r\n\r\n"+checkErr.Error(), walk.MsgBoxIconError)
				return
			}
			if checkErr != nil {
				walk.MsgBox(ui.window, "语言推理预检失败", "外接软件已执行真实的 chat/completions 请求，但中转站未成功返回。洛水暂不启动。\r\n\r\n"+checkErr.Error(), walk.MsgBoxIconError)
				_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 语言推理预检失败：" + checkErr.Error() + "\r\n"))
				return
			}
			_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 语言推理真实请求预检通过\r\n"))
			ui.launchLuoshuiChecked()
		})
	}(cfg.Profiles.Chat)
}

func (ui *DesktopUI) launchLuoshuiChecked() {
	root, err := FindLuoshuiRoot(ui.appDir)
	if err != nil {
		walk.MsgBox(ui.window, "未找到洛水", err.Error(), walk.MsgBoxIconError)
		return
	}
	if ui.autoConfig == nil || ui.autoConfig.Checked() {
		changed, err := ConfigureLuoshuiExternalProxy(root)
		if err != nil {
			walk.MsgBox(ui.window, "自动配置失败", err.Error(), walk.MsgBoxIconError)
			return
		}
		if changed {
			_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 已自动配置洛水外接接口（原 settings.json 已备份）\r\n"))
		}
	}
	exe := filepath.Join(root, "洛水.exe")
	activeMarker := filepath.Join(root, "huiju_runtime_patch.active")
	_ = os.Remove(activeMarker)
	command := exec.Command(exe)
	command.Dir = root
	pythonPath := root
	if existing := os.Getenv("PYTHONPATH"); existing != "" {
		pythonPath += string(os.PathListSeparator) + existing
	}
	command.Env = append(os.Environ(), "LUOSHUI_USE_EXTERNAL_PROXY=1", "PYTHONPATH="+pythonPath, "NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1")
	if err := command.Start(); err != nil {
		walk.MsgBox(ui.window, "启动失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	go ui.verifyRuntimePatch(activeMarker)
}

func (ui *DesktopUI) verifyRuntimePatch(activeMarker string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ui.stopRefresh:
			return
		case <-ticker.C:
			if _, err := os.Stat(activeMarker); err == nil {
				_, _ = ui.logBuffer.Write([]byte(time.Now().Format("2006/01/02 15:04:05") + " 洛水运行时代理补丁已确认加载\r\n"))
				return
			}
		case <-timeout.C:
			ui.window.Synchronize(func() {
				walk.MsgBox(ui.window, "洛水补丁未加载", "洛水已经启动，但运行时代理补丁没有加载。请关闭洛水后重新点击“启动洛水”，并将运行日志发给管理员。", walk.MsgBoxIconError)
			})
			return
		}
	}
}

func (ui *DesktopUI) openFolder() {
	_ = exec.Command("explorer.exe", ui.appDir).Start()
}
