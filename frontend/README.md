# agents-otel-monitor Web Dashboard

React + TypeScript + Vite 实现的本地 harness 遥测看板。界面通过现有 `client=all|claude|codex` API 参数在全部 harness、Claude Code 与 Codex CLI 之间切换；`client` 是后端兼容参数名，对应领域术语 harness family。

## 本地开发

先在仓库根目录启动 Go server，使查询 API 监听 `127.0.0.1:9100`：

```bash
go build -o bin/server ./cmd/server
cp config.example.yaml config.yaml
./bin/server -config config.yaml
```

再启动 Vite：

```bash
cd frontend
npm ci
npm run dev
```

开发服务监听 `http://127.0.0.1:5173`，`/api` 与 `/internal` 请求会代理到 Go server。

## 构建与检查

```bash
npm test
npm run lint
npm run build
```

`npm run build` 将产物写入 `internal/web/dist/`，供 Go server 通过 `go:embed` 打入单二进制。仓库根目录的 `scripts/build-all.sh` 会依次完成前端构建与后端编译。
