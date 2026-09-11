# 构建 MosDNS-x

## 无网页界面的构建

普通 Go 构建不依赖 Node.js，也不要求存在 `web/dist`：

```sh
go build -o mosdns .
```

批量生成原有平台的无界面发布包：

```sh
python3 release.py --headless
```

## 包含网页界面的构建

Node.js 仅在生成前端资源时需要；使用 Node 22.22.2 或更新的 22.x，CI 使用 22.x。依赖版本由 `web/package-lock.json` 锁定：

```sh
cd web
npm ci
npm run build
cd ..
go build -tags ui -o mosdns .
```

`-tags ui` 会把 `web/dist` 中的完整管理界面嵌入 Go 二进制。运行时只需部署这个二进制，不需要 Node.js，也不需要单独复制静态文件。`web/dist` 是构建产物，不提交到仓库；如果尚未执行前端构建，带 `ui` 标签的 Go 构建会明确失败。

默认运行发布脚本会先构建一次前端，然后复用这些资源生成所有平台包：

```sh
python3 release.py
```

## 前端开发

```sh
cd web
npm ci
npm run dev
```

Vite 开发服务器把 `/api` 请求转发至 `127.0.0.1:18080`。开发联调配置应设置 `api.http: 127.0.0.1:18080`、`control.development: true`，并将 `control.panel_origin` 设为实际打开的 Vite 地址（例如 `http://localhost:5173`），以通过 Origin 校验。仓库的 `control-local.yaml` 使用嵌入界面与端口 `18081`；用于 Vite 联调时需复制后调整这些字段。
