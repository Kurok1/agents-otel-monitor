# Vibecoding Monitor

面向 macOS 的菜单栏状态看板。项目与 `frontend/` 完全隔离，界面使用 React + TypeScript，原生壳使用 Tauri 2。

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

## 版本

仓库根目录的 `VERSION` 是项目版本源。当前值 `3.0` 注入后端 `/version`；Cargo、npm 与 macOS bundle 使用对应的 SemVer `3.0.0`。

## 运行约定

- 默认连接 `http://127.0.0.1:9100`，HTTP 请求由 Rust 原生层发出。
- Host、Port、主题、客户端和统计周期在每次启动时恢复默认值，不写入本地配置。
- “登录时启动”由 macOS 登录项管理，是唯一允许跨启动保留的设置。
- 面板隐藏时停止轮询；展开、切换客户端/周期或手动刷新时立即请求，持续显示期间每 5 分钟刷新一次。
