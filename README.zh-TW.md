# Mosdns-x（多使用者 DoH 服務版）

[简体中文](README.md) | [English](README.en.md) | **繁體中文** | [日本語](README.ja.md)

[![Release](https://img.shields.io/github/v/release/MiCat-S/mosdns-x?include_prereleases&label=release)](https://github.com/MiCat-S/mosdns-x/releases)

本儲存庫 Fork 自 [pmkol/mosdns-x](https://github.com/pmkol/mosdns-x)。Mosdns-x 是以 Go 撰寫的高效能 DNS 轉發器，透過外掛管線自訂 DNS 處理邏輯，支援 UDP、TCP、DoT、DoQ、DoH 與 DoH3。

本 Fork 在此基礎上加入了**多使用者 DoH / DoH3 服務**：一個 Mosdns 程序同時提供 DNS、管理 API 與網頁面板。管理員開設帳號、分配額度；使用者為每台裝置產生專屬的加密 DNS 位址，並在自己的面板中查看用量與查詢記錄。所有使用者共用主設定中的同一套 DNS 處理邏輯。

> 網頁面板與 `docs/` 下的文件目前只有簡體中文版。

## 功能

**帳號與計量**

- 管理員可開設、編輯、停用與刪除帳號，設定到期時間、每日／每月額度、QPS 與突發量、裝置數上限。刪除帳號時會一併清除該使用者的所有資料。
- 每台裝置一組 UUID 憑證，可透過專屬 URL 或 `Authorization: Bearer` 連線；憑證可隨時輪替或撤銷。
- 用量依全域、使用者、裝置三個層級精確計量，額度扣除與請求受理在同一筆交易中完成。

**使用者面板**

- 查看各裝置的 DNS 位址與憑證，自行建立、輪替或撤銷憑證。
- 查看自己的用量統計與查詢記錄。
- 修改密碼，查看說明與連線設定方式。

**記錄與維運**

- 詳細查詢記錄：由全域 `control.query_log` 開關控制，保留時間與筆數上限在 `control.telemetry` 中設定；每筆記錄標明回應來自上游還是快取，以及使用的上游。
- 用量統計圖表、稽核記錄、系統健康監控，以及需驗證的 Prometheus `/metrics`。
- 控制資料與統計資料可存放於 bbolt（無外部相依），也可存放於 MySQL 5.7 / 8.4。
- 託管執行設定：在面板中驗證、熱更新與回溯上游、快取及統計保留策略，失敗時不影響正在執行的設定。
- 離線指令 `control init-admin`、`control status`，以及用於救回管理員帳號的 `control reset-password`。

早期版本中的使用者 DNS 策略（自訂規則、安全與隱私選項、回應最佳化等）、公共清單、DNS Lookup，以及 `control backup`／`restore`／`migrate-mysql` 指令已移除。升級與回溯說明見[儲存文件](docs/storage.md#已移除功能的数据)（簡體中文）。

## 快速開始

在使用 systemd 的 Linux 主機上安裝或升級：

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

指令碼會：

- 偵測 CPU 架構，下載本 Fork 的 Release 並驗證 SHA-256。
- 安裝 `/usr/local/bin/mosdns` 與 systemd 服務，然後啟動服務。
- 不覆寫既有的 `/etc/mosdns/config.yaml`。
- 從中國大陸連線時，安裝套件預設經由 `gh-proxy.com` 下載，但校驗檔一律直接從 GitHub 取得。

它不會安裝反向代理、建立網域或設定管理員密碼，詳見[一鍵安裝](docs/installation.md)。

預設設定只是一個監聽 `127.0.0.1:5533` 的一般 DNS 轉發器，**不含多使用者模式**。啟用多使用者服務：

1. 在設定中加入 `control` 區段，寫明公共 DNS 位址與面板位址，見[多使用者設定](docs/service-config.md)。
2. 以 `mosdns control init-admin` 建立第一個管理員，密碼只從標準輸入讀取，見[維運文件](docs/operations.md#本地初始化与启动)。
3. 以同一台主機上的反向代理為面板與 DoH 提供 HTTPS，見[部署教學：Ubuntu / Debian + Caddy](docs/deployment.md)。
4. 以管理員身分開啟 `/admin` 開設使用者。使用者登入 `/app` 後，在帳號頁建立裝置憑證，並把取得的位址填入系統或瀏覽器的加密 DNS 設定。

檢查多使用者模式是否就緒：

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

## 文件

以下文件皆為簡體中文。

| 主題 | 文件 |
|---|---|
| 安裝與升級 | [一鍵安裝](docs/installation.md)、[部署教學](docs/deployment.md) |
| 設定 | [多使用者設定](docs/service-config.md)、[儲存與備份](docs/storage.md) |
| 日常維運 | [初始化、備份、託管設定與密碼重設](docs/operations.md)、[安全與執行監控](docs/monitoring.md) |
| 開發 | [服務架構](docs/service-architecture.md)、[API 規格](docs/control-api.md)、[建置](docs/building.md)、[發布](docs/releasing.md) |
| 驗收 | [開發審查與驗收](docs/development-review.md)、[效能驗證](docs/performance.md) |

## 適用範圍

- 以單機、單一執行個體部署與驗收。MySQL 後端的多執行個體並行與容錯移轉尚未經過正式環境驗證。
- Release 由維護者在本機建置後上傳，沒有簽章或 provenance 證明。對供應鏈有更高要求的部署，請依[建置說明](docs/building.md)自行建置。
- 主設定仍以檔案為準；面板只能修改不含敏感參數的託管部分。

## 上游與社群

- 原版 Mosdns-x 的功能說明、外掛設定與教學請見[上游 Wiki](https://github.com/pmkol/mosdns-x/wiki)，原版預先編譯檔請見[上游 Release](https://github.com/pmkol/mosdns-x/releases)。本 Fork 的安裝套件請使用[本儲存庫 Release](https://github.com/MiCat-S/mosdns-x/releases)。
- Telegram 社群（上游）：[Mosdns-x Group](https://t.me/mosdns)
- [easymosdns](https://github.com/pmkol/easymosdns)：適用於 Linux 的輔助指令碼，幾分鐘內即可架設支援 ECS 的無汙染 DNS 伺服器，內建針對中國大陸的最佳化規則。
- [mosdns v4](https://github.com/IrineSistiana/mosdns/tree/v4)：外掛化的 DNS 轉發器，為 Mosdns-x 的上游專案。
