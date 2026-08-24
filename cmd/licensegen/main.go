package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"huiju-bridge/internal/licensing"
)

var machinePattern = regexp.MustCompile(`^HJ-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}-[A-Z0-9]{5}$`)

type signerUI struct {
	window       *walk.MainWindow
	machine      *walk.LineEdit
	customer     *walk.LineEdit
	days         *walk.ComboBox
	expires      *walk.Label
	keyStatus    *walk.Label
	resultStatus *walk.Label
	code         *walk.TextEdit
	privateKey   ed25519.PrivateKey
	appDir       string
	outputDir    string
}

var durationLabels = []string{"30 天", "90 天", "180 天", "365 天", "730 天", "3650 天"}
var durationDays = []int{30, 90, 180, 365, 730, 3650}

func main() {
	executable, err := os.Executable()
	if err != nil {
		panic(err)
	}
	appDir := filepath.Dir(executable)
	ui := &signerUI{appDir: appDir, outputDir: filepath.Join(appDir, "已签发授权")}
	privateValue, keyErr := os.ReadFile(filepath.Join(appDir, "license_private.key"))
	if keyErr == nil {
		ui.privateKey, keyErr = licensing.DecodePrivateKey(string(privateValue))
	}
	if err := ui.run(keyErr); err != nil {
		panic(err)
	}
}

func (ui *signerUI) run(keyErr error) error {
	window := MainWindow{
		AssignTo: &ui.window,
		Title:    "荟聚 API 授权签发工具",
		MinSize:  Size{Width: 760, Height: 620},
		Size:     Size{Width: 860, Height: 700},
		Layout:   VBox{Margins: Margins{Left: 18, Top: 16, Right: 18, Bottom: 16}, Spacing: 8},
		Children: []Widget{
			Label{Text: "荟聚 API 客户端授权", Font: Font{PointSize: 14, Bold: true}},
			Label{AssignTo: &ui.keyStatus, Text: "正在检查签发密钥..."},
			VSpacer{Size: 4},
			Label{Text: "客户机器码"},
			LineEdit{AssignTo: &ui.machine, ToolTipText: "粘贴客户在客户端“授权管理”页复制的完整机器码"},
			Label{Text: "客户名称"},
			LineEdit{AssignTo: &ui.customer},
			Label{Text: "授权有效天数"},
			ComboBox{
				AssignTo:              &ui.days,
				Editable:              false,
				Model:                 durationLabels,
				CurrentIndex:          3,
				OnCurrentIndexChanged: ui.updateExpiry,
			},
			Label{AssignTo: &ui.expires},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{Text: "生成授权", OnClicked: ui.generate},
					PushButton{Text: "复制授权码", OnClicked: ui.copyCode},
					PushButton{Text: "打开授权目录", OnClicked: ui.openOutputDir},
					HSpacer{},
				},
			},
			Label{AssignTo: &ui.resultStatus, Text: "尚未生成授权"},
			Label{Text: "授权码"},
			TextEdit{AssignTo: &ui.code, ReadOnly: true, VScroll: true, MinSize: Size{Width: 500, Height: 210}},
			Label{Text: "私钥仅保存在本机授权管理目录，不要把此工具和私钥发送给客户。"},
		},
	}
	if err := window.Create(); err != nil {
		return err
	}
	ui.updateExpiry()
	if keyErr != nil {
		_ = ui.keyStatus.SetText("签发密钥不可用：" + keyErr.Error())
		ui.keyStatus.SetTextColor(walk.RGB(170, 45, 35))
	} else {
		_ = ui.keyStatus.SetText("签发密钥已就绪")
		ui.keyStatus.SetTextColor(walk.RGB(18, 128, 70))
	}
	ui.window.Show()
	ui.window.Run()
	return nil
}

func (ui *signerUI) updateExpiry() {
	if ui.days == nil || ui.expires == nil {
		return
	}
	expires := time.Now().Add(time.Duration(ui.selectedDays()) * 24 * time.Hour)
	_ = ui.expires.SetText("预计到期时间：" + expires.Format("2006-01-02 15:04:05"))
}

func (ui *signerUI) generate() {
	if len(ui.privateKey) != ed25519.PrivateKeySize {
		walk.MsgBox(ui.window, "无法签发", "签发私钥不可用，请确认 license_private.key 与本工具位于同一目录。", walk.MsgBoxIconError)
		return
	}
	machine := strings.ToUpper(strings.TrimSpace(ui.machine.Text()))
	customer := strings.TrimSpace(ui.customer.Text())
	days := ui.selectedDays()
	if !machinePattern.MatchString(machine) {
		walk.MsgBox(ui.window, "机器码无效", "请粘贴完整机器码，例如 HJ-XXXXX-XXXXX-XXXXX-XXXXX。", walk.MsgBoxIconWarning)
		return
	}
	if customer == "" {
		walk.MsgBox(ui.window, "客户名称为空", "请输入客户名称。", walk.MsgBoxIconWarning)
		return
	}
	if days < 1 {
		walk.MsgBox(ui.window, "授权天数无效", "授权天数必须大于 0。", walk.MsgBoxIconWarning)
		return
	}
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		walk.MsgBox(ui.window, "生成失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	now := time.Now()
	claims := licensing.Claims{
		Product:     licensing.Product,
		LicenseID:   strings.ToUpper(hex.EncodeToString(idBytes)),
		Customer:    customer,
		MachineCode: machine,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(time.Duration(days) * 24 * time.Hour).Unix(),
	}
	code, err := licensing.Sign(ui.privateKey, claims)
	if err != nil {
		walk.MsgBox(ui.window, "生成失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := os.MkdirAll(ui.outputDir, 0700); err != nil {
		walk.MsgBox(ui.window, "保存失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	filename := fmt.Sprintf("%s-%s-%s.txt", safeFilename(customer), machine, now.Format("20060102-150405"))
	outputPath := filepath.Join(ui.outputDir, filename)
	if err := os.WriteFile(outputPath, []byte(code), 0600); err != nil {
		walk.MsgBox(ui.window, "保存失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	_ = ui.code.SetText(code)
	expires := time.Unix(claims.ExpiresAt, 0).Format("2006-01-02 15:04:05")
	_ = ui.resultStatus.SetText(fmt.Sprintf("生成成功 · 客户：%s · 到期：%s · 已保存：%s", customer, expires, filename))
	ui.resultStatus.SetTextColor(walk.RGB(18, 128, 70))
}

func (ui *signerUI) selectedDays() int {
	index := ui.days.CurrentIndex()
	if index < 0 || index >= len(durationDays) {
		return 365
	}
	return durationDays[index]
}

func safeFilename(value string) string {
	replacer := strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	value = strings.TrimSpace(replacer.Replace(value))
	if value == "" {
		return "客户"
	}
	return value
}

func (ui *signerUI) copyCode() {
	code := strings.TrimSpace(ui.code.Text())
	if code == "" {
		walk.MsgBox(ui.window, "没有授权码", "请先生成授权。", walk.MsgBoxIconWarning)
		return
	}
	if err := walk.Clipboard().SetText(code); err != nil {
		walk.MsgBox(ui.window, "复制失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	_ = ui.resultStatus.SetText("授权码已复制到剪贴板")
}

func (ui *signerUI) openOutputDir() {
	if err := os.MkdirAll(ui.outputDir, 0700); err != nil {
		walk.MsgBox(ui.window, "打开失败", err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := exec.Command("explorer.exe", ui.outputDir).Start(); err != nil {
		walk.MsgBox(ui.window, "打开失败", err.Error(), walk.MsgBoxIconError)
	}
}
