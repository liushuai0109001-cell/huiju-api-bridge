# 荟聚 API 外接软件（Go 桌面版）

本软件使用 Go 编写，通过原生 Windows UI 管理洛水 `v2.3.4` 的语言、图片和视频请求，并转发到你自己的 NewAPI/中转站。

## 客户端授权

- 客户端使用 Ed25519 数字签名授权，只内置授权公钥。
- 授权绑定 Windows 机器码并包含到期时间，授权文件不能复制到其他电脑使用。
- 未授权或授权过期时，代理服务不会启动；运行期间的请求入口也会再次校验并拒绝未授权请求。
- 签发工具和私钥保存在 `授权管理` 目录，该目录严禁进入客户发行包。

## 已确认的洛水链路

| 类型 | 洛水固定入口/模型 | 本地接管接口 | 转发接口 |
|---|---|---|---|
| 语言 | `gemini-3.1-pro-preview-洛水` 等洛水推理模型 | `/v1/chat/completions` | 上游 `/v1/chat/completions` |
| 图片 | `luoshui_nano_banana`、`nano2-出图包` | `/v1/images` 或 `/v1/images/generations` | 上游 `/v1/images/generations` |
| 视频 | `本地Grok`、`grok-imagine-1.0-video` | `/v1/videos`、`/v1/videos/{id}` | 上游同路径 |

漫剧解说、剧本创作和自由/超级画布复用上述三类协议，不需要为三个界面分别实现 API。

## 使用

1. 双击 `荟聚API外接软件.exe` 或 `启动荟聚API外接软件.bat`。
2. 在“语言模型 / 图片模型 / 视频模型”分页分别填写 Base URL、API Key，然后点击“获取模型”并从下拉框选择目标模型。
3. 点击“保存配置”。服务会自动重启并应用新配置。
4. 图片比例、视频比例和视频时长都使用界面选项配置，不需要填写 JSON。
5. 点击“启动洛水”，软件会携带外接代理环境变量启动洛水。

程序打开后自动监听 `127.0.0.1:5400` 和 `127.0.0.1:8000`。关闭窗口会停止两个监听服务。

## 自动配置洛水

界面底部默认勾选“自动配置洛水外接接口”。外接软件自身启动时会立即写入洛水接口，点击“启动洛水”前会再次校验。程序会自动设置洛水的语言、图片、视频和图床本地接口，不需要再进入洛水逐项填写。

点击“启动洛水”时，外接软件会自动保存当前表单、重启代理服务，并向中转站预检语言模型密钥。若中转站返回 HTTP 401，软件会直接提示“语言密钥无效”并阻止启动洛水，避免角色推理批量失败。

外接软件还会自动安装内置的洛水运行时重定向模块，用于接管编译模块中仍然硬编码到官方站点的推理请求。安装时会生成 `huiju_luoshui_proxy_patch.py`，并在 `sitecustomize.py` 中加入加载器；已有 `sitecustomize.py` 会备份为 `sitecustomize.py.bak_before_huiju`。这一步是用户电脑与开发电脑保持相同行为所必需的。

为兼容未自动加载 `sitecustomize.py` 的洛水环境，程序还会在 `run.py` 主程序入口前安装显式加载器，并备份为 `run.py.bak_before_huiju`。洛水启动后 20 秒内会校验补丁激活标记；未加载时会在界面直接报错。

语言推理默认采用“上游流式、本地聚合”模式。洛水仍接收普通 JSON，但外接软件通过流式响应保持中转站连接，避免长推理在约 120 秒时被网关以 HTTP 524 中断。运行日志出现 `CHAT protocol: upstream stream aggregation` 表示该机制已启用。

剧本模式的视频参考图会由洛水以 `reference_images` 等不同字段提交。外接软件会去重并统一补充为 NewAPI 使用的 `images` 数组，同时保留原字段。日志中的 `VIDEO submit request=... references=N` 与 `VIDEO submitted ... upstream_task=... references=N` 必须属于同一个请求；只有这里的 `N` 才是最终上游视频任务实际携带的参考图数量。

视频详细日志会依次显示 `UPLOAD 开始/成功/失败`、`VIDEO 收到请求`、`VIDEO 提交成功`、`VIDEO 状态查询`、`VIDEO 成功：视频文件已生成`。状态查询会显示 `task`、`stage`、`status`、`progress` 和错误详情，便于区分图床失败、任务未带图、上游排队、生成失败和视频回传失败。

程序只修改 `data/settings.json` 中与外接桥有关的字段，首次修改前会生成 `data/settings.json.bak_external_proxy`。取消该选项后，启动洛水时不会修改配置。

## 自动更新

客户端启动后会读取公开更新清单 `latest.json`，默认地址为 `https://raw.githubusercontent.com/liushuai0109001-cell/huiju-api-bridge/main/latest.json`；也可通过 `config.json` 的 `update.manifest_url` 或环境变量 `HUIJU_UPDATE_MANIFEST_URL` 指向 GitHub Raw、GitHub Pages、CNB 或其他 CDN。清单格式如下：

```json
{
  "version": "1.0.11",
  "download_url": "https://raw.githubusercontent.com/liushuai0109001-cell/huiju-api-bridge/main/downloads/huiju-api-bridge-v1.0.12-windows-amd64.zip",
  "sha256": "发行包 SHA256",
  "notes": "更新说明"
}
```

客户端会并发探测 `download_urls` 中的多个镜像，优先打开最先响应的地址；GitHub 慢或不可达时会自动尝试 CNB 和代理 CDN。客户端只提示并打开下载地址，不会覆盖正在运行的程序。发布新版本时上传 ZIP、更新 `latest.json`，用户下次启动即可检测到更新。发行包不包含授权私钥、客户授权文件或 API Key。

## 开发与验证

```powershell
go test ./...
go vet ./...
Invoke-RestMethod http://127.0.0.1:5400/health
```

正式发行使用：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\build-release.ps1 -Version 1.0.10
```

脚本会执行测试并生成不含 API Key、日志、本机授权和签发私钥的 ZIP 客户发行包。

源码入口为 `main.go`，协议服务在 `proxy.go`，Windows UI 在 `ui.go`。日志保存在 `bridge.log`；默认不记录请求正文，避免剧本内容和密钥进入日志。
