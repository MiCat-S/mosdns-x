# Mosdns-x（マルチユーザー DoH サービス版）

[简体中文](README.md) | [English](README.en.md) | [繁體中文](README.zh-TW.md) | **日本語**

[![Release](https://img.shields.io/github/v/release/MiCat-S/mosdns-x?include_prereleases&label=release)](https://github.com/MiCat-S/mosdns-x/releases)

このリポジトリは [pmkol/mosdns-x](https://github.com/pmkol/mosdns-x) のフォークです。Mosdns-x は Go で書かれた高性能な DNS フォワーダーで、プラグインのパイプラインで DNS の処理を自由に組み立てられます。UDP、TCP、DoT、DoQ、DoH、DoH3 に対応しています。

このフォークでは、そこに**マルチユーザー向けの DoH / DoH3 サービス**を追加しています。1 つの Mosdns プロセスが DNS、管理 API、Web パネルをまとめて提供します。管理者はアカウントを作成してクォータを割り当て、ユーザーはデバイスごとに専用の暗号化 DNS アドレスを発行し、自分のパネルで使用量とクエリログを確認できます。DNS の処理はメイン設定で定義したものを全ユーザーが共有します。

> Web パネルと `docs/` 以下のドキュメントは簡体字中国語のみです。

## 機能

**アカウントと計量**

- 管理者はアカウントの作成・編集・停止・削除ができ、有効期限、日次／月次のクォータ、QPS とバースト、デバイス数の上限を設定できます。アカウントを削除すると、そのユーザーのデータもすべて消去されます。
- デバイスごとに UUID の認証情報を発行し、専用 URL または `Authorization: Bearer` で接続します。認証情報はいつでもローテーション・失効できます。
- 使用量はサーバー全体、ユーザー、デバイスの 3 段階で正確に計量され、クォータの消費はクエリの受け付けと同じトランザクションで行われます。

**ユーザーパネル**

- 各デバイスの DNS アドレスと認証情報を確認し、認証情報の作成・ローテーション・失効を自分で行えます。
- 自分の使用量統計とクエリログを確認できます。
- パスワードの変更、ヘルプと接続設定の説明の参照ができます。

**ログと運用**

- 詳細なクエリログ：グローバルな `control.query_log` スイッチで有効化し、保持期間と件数の上限は `control.telemetry` で設定します。各レコードには、どの送信先 DNS が応答したか（キャッシュヒット時は元のアップストリーム）、どの振り分けルールを通ったか、アップストリームへの各リクエストの所要時間と結果が記録されます。プライベートなアップストリームはプラグイン名と番号、または設定したラベルでのみ表示され、ドメインや IP は表示されません。
- 使用量の統計グラフ、監査ログ、システムのヘルスモニタリング、認証付きの Prometheus `/metrics`。
- 制御データと統計データは bbolt（外部依存なし）または MySQL 5.7 / 8.4 に保存できます。
- 管理対象の実行時設定：アップストリーム、キャッシュ、統計の保持期間を、パネルから検証・ホットリロード・ロールバックできます。変更が失敗しても、実行中の設定には影響しません。
- オフラインコマンド `control init-admin`、`control status`、および管理者がログインできなくなったときのための `control reset-password`。

以前のバージョンにあったユーザー別 DNS ポリシー（カスタムルール、セキュリティとプライバシーの設定、応答の最適化など）、公開ブロックリスト、DNS Lookup、`control backup`／`restore`／`migrate-mysql` コマンドは削除されました。アップグレードとロールバックの注意点は[ストレージ](docs/storage.md#已移除功能的数据)（簡体字中国語）を参照してください。

## クイックスタート

systemd を使う Linux ホストでインストールまたはアップグレードします。

```bash
curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
```

このスクリプトは次のことを行います。

- CPU アーキテクチャを判別し、このフォークの Release をダウンロードして SHA-256 を検証します。
- `/usr/local/bin/mosdns` と systemd サービスをインストールし、サービスを起動します。
- 既存の `/etc/mosdns/config.yaml` は上書きしません。
- 中国本土からの接続では、パッケージを既定で `gh-proxy.com` 経由でダウンロードしますが、チェックサムは常に GitHub から直接取得します。

リバースプロキシのインストール、ドメインの作成、管理者パスワードの設定は行いません。詳しくは[ワンコマンドインストール](docs/installation.md)を参照してください。

既定の設定は `127.0.0.1:5533` で待ち受ける通常の DNS フォワーダーで、**マルチユーザーモードは含まれていません**。マルチユーザーサービスを有効にするには：

1. 設定に `control` セクションを追加し、公開 DNS の URL とパネルのオリジンを記述します。[マルチユーザー設定](docs/service-config.md)を参照してください。
2. `mosdns control init-admin` で最初の管理者を作成します。パスワードは標準入力からのみ読み込まれます。[運用ドキュメント](docs/operations.md#本地初始化与启动)を参照してください。
3. 同じホスト上のリバースプロキシで、パネルと DoH を HTTPS で提供します。[デプロイ手順：Ubuntu / Debian + Caddy](docs/deployment.md) を参照してください。
4. 管理者として `/admin` を開き、ユーザーを作成します。ユーザーは `/app` にログインし、アカウントページでデバイスの認証情報を作成して、得られたアドレスを OS やブラウザの暗号化 DNS 設定に入力します。

マルチユーザーモードの準備ができているか確認します。

```bash
mosdns control status --config /etc/mosdns/config.yaml
```

## ドキュメント

ドキュメントはすべて簡体字中国語です。

| 分野 | ドキュメント |
|---|---|
| インストールとアップグレード | [ワンコマンドインストール](docs/installation.md)、[デプロイ手順](docs/deployment.md) |
| 設定 | [マルチユーザー設定](docs/service-config.md)、[ストレージとバックアップ](docs/storage.md) |
| 運用 | [初期化、バックアップ、管理対象設定、パスワードのリセット](docs/operations.md)、[セキュリティと稼働監視](docs/monitoring.md) |
| 開発 | [サービス構成](docs/service-architecture.md)、[API 仕様](docs/control-api.md)、[ビルド](docs/building.md)、[リリース](docs/releasing.md) |
| 受け入れ | [開発レビューと受け入れ](docs/development-review.md)、[性能検証](docs/performance.md) |

## 対象範囲

- 単一ホスト・単一インスタンスでのデプロイと検証を前提としています。MySQL バックエンドでの複数インスタンスの同時稼働やフェイルオーバーは、本番環境向けには検証されていません。
- Release はメンテナーがローカルでビルドして手動でアップロードしたもので、署名や provenance の証明はありません。サプライチェーンの要件が厳しい場合は、[ビルド手順](docs/building.md)に従って自分でビルドしてください。
- メインの設定はあくまで設定ファイルです。パネルから変更できるのは、機密情報を含まない管理対象の部分だけです。

## アップストリームとコミュニティ

- オリジナルの Mosdns-x の機能、プラグイン設定、チュートリアルは[アップストリームの Wiki](https://github.com/pmkol/mosdns-x/wiki)、オリジナルのビルド済みバイナリは[アップストリームの Release](https://github.com/pmkol/mosdns-x/releases) にあります。このフォークのパッケージは[このリポジトリの Release](https://github.com/MiCat-S/mosdns-x/releases) を使ってください。
- Telegram コミュニティ（アップストリーム）：[Mosdns-x Group](https://t.me/mosdns)
- [easymosdns](https://github.com/pmkol/easymosdns)：Linux 向けの補助スクリプトです。ECS に対応した汚染のない DNS サーバーを数分で構築でき、中国本土向けに最適化したルールを内蔵しています。
- [mosdns v4](https://github.com/IrineSistiana/mosdns/tree/v4)：プラグイン型の DNS フォワーダーで、Mosdns-x のアップストリームプロジェクトです。
