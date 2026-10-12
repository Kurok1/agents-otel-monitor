# Vibecoding Monitor（agents-otel-monitor Desktop）

`agents-otel-monitor` 面向 macOS 的菜单栏状态看板，可在全部 harness、Claude Code 与 Codex CLI 视图之间切换。项目与 `frontend/` 完全隔离，界面使用 React + TypeScript，原生壳使用 Tauri 2。

## 本地开发

需要 Node.js 22.13+、Rust 1.85+ 和 Xcode Command Line Tools。首次安装依赖后即可运行：

```bash
npm ci
npm test
npm run desktop:dev
```

构建当前机器可用的 `.app`：

```bash
npm run desktop:build
```

产物位于 `src-tauri/target/release/bundle/macos/Vibecoding Monitor.app`。

## GitHub 打包与发布

桌面端预编译包仅支持 **Apple Silicon（arm64）**。`.github/workflows/desktop-macos.yml` 使用 macOS runner 和固定的 `aarch64-apple-darwin` 目标，先校验版本、运行前端 lint / 测试，再构建 `.app` / DMG 并测试 Rust 原生层。打包时检查 App 版本、arm64 架构、代码签名和 DMG 完整性。

- 推送与 `VERSION` 对应的 `v*` 标签时，Release 工作流复用桌面端构建；桌面端与服务端构建均成功后，将安装包和统一 `checksums.txt` 上传到同一个 GitHub Release。
- 修改 `desktop/`、版本文件或发布工作流的 PR 会运行相同构建，将结果保存在 `desktop-darwin-arm64` Actions artifact 中，保留 7 天。
- 在 GitHub Actions 的 **macOS Desktop → Run workflow** 中选择分支或标签，可以手动构建。手动运行只生成 Actions artifact，不创建 Release；所选 ref 必须包含此工作流。

发布附件：

| 文件 | 用途 |
|---|---|
| `agents-otel-monitor-desktop_v<版本>_darwin-arm64.dmg` | 打开后将 `Vibecoding Monitor.app` 拖入 Applications |
| `agents-otel-monitor-desktop_v<版本>_darwin-arm64.app.zip` | 保留 App bundle 结构的压缩包 |
| `checksums.txt` | 桌面端及服务端附件的 SHA-256 校验和 |

当前流程使用 [Tauri ad-hoc 签名](https://v2.tauri.app/distribute/sign/macos/#ad-hoc-signing)，不需要配置 Apple 证书，但尚未经过 Apple notarization。首次打开下载的 App 时，macOS 可能要求在「系统设置 → 隐私与安全性」中允许打开。桌面端仍需连接已运行的监控服务，安装包不包含 Go 服务端。

在 Apple Silicon Mac 上复现 CI 打包命令：

```bash
npm ci
APPLE_SIGNING_IDENTITY=- npm run desktop:build -- --target aarch64-apple-darwin --bundles app,dmg --ci -- --locked
bash scripts/package-macos.sh
```

安装包与独立校验文件保存在 `desktop/release-artifacts/`。此流程从合并后推送的新标签生效，不会自动补充已经发布的旧 Release。

## 版本

仓库根目录的 `VERSION` 是项目版本源，注入后端 `/version`；Cargo、npm 与 macOS bundle 使用对应的 SemVer。`npm run version:check` 校验三处版本，Tauri 构建会通过 `prebuild` 自动执行。

## 运行约定

- 默认连接 `http://127.0.0.1:9100`，HTTP 请求由 Rust 原生层发出。
- Host、Port、主题、客户端和统计周期在每次启动时恢复默认值，不写入本地配置。
- “登录时启动”由 macOS 登录项管理，是唯一允许跨启动保留的设置。
- 面板隐藏时停止轮询；展开、切换客户端/周期或手动刷新时立即请求，持续显示期间每 5 分钟刷新一次。
