# CNB 发布与自动更新

## 建议仓库结构

将本目录安全源码推送到 CNB 仓库，例如：

```text
https://cnb.cool/cnb.c0zjU0U6wHA/huiju-api-bridge
```

不要提交 `config.json`、`license.dat`、`bridge.log`、`授权管理/license_private.key` 或 `授权管理/已签发授权`。`.gitignore` 已默认排除这些内容。

## 推送

在 CNB 完成登录并创建仓库后，在本目录执行：

```powershell
git init
git add .
git commit -m "release: huiju api bridge"
git branch -M main
git remote add origin https://cnb.cool/cnb.c0zjU0U6wHA/huiju-api-bridge.git
git push -u origin main
```

## 发布包与更新清单

将 `release/huiju-api-bridge-v1.0.11-windows-amd64.zip` 上传为 CNB Release 附件，计算 SHA256：

```powershell
Get-FileHash .\release\huiju-api-bridge-v1.0.11-windows-amd64.zip -Algorithm SHA256
```

把 `latest.json.example` 复制为 `latest.json`，替换版本、下载地址、SHA256 和更新说明后，提交到仓库根目录。客户端默认读取 CDN 地址；部署到 CNB 后，在客户 `config.json` 中将 `update.manifest_url` 改为仓库的 Raw 文件地址，例如：

```json
{
  "enabled": true,
  "manifest_url": "https://cnb.cool/cnb.c0zjU0U6wHA/huiju-api-bridge/-/raw/main/latest.json",
  "check_on_start": true
}
```

也可以设置环境变量 `HUIJU_UPDATE_MANIFEST_URL`，它优先于配置文件，适合统一切换 GitHub、CNB 或自有 CDN。
