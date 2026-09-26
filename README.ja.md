# Mosdns-x（マルチユーザー DoH サービス版）

[简体中文](README.md) | [English](README.en.md) | [繁體中文](README.zh-TW.md) | **日本語**

[![Release](https://img.shields.io/github/v/release/MiCat-S/mosdns-x?include_prereleases&label=release)](https://github.com/MiCat-S/mosdns-x/releases)

このリポジトリは [pmkol/mosdns-x](https://github.com/pmkol/mosdns-x) のフォークです。Mosdns-x は Go で書かれた高性能な DNS フォワーダーで、プラグインのパイプラインで DNS の処理を自由に組み立てられます。UDP、TCP、DoT、DoQ、DoH、DoH3 に対応しています。

このフォークでは、そこに**マルチユーザー向けの DoH / DoH3 サービス**を追加しています。1 つの Mosdns プロセスが DNS、管理 API、Web パネルをまとめて提供します。管理者はアカウントを作成してクォータを割り当て、ユーザーはデバイスごとに専用の暗号化 DNS アドレスを発行し、自分のパネルでブロックルール、プライバシー設定、クエリログを調整できます。

> Web パネルと `docs/` 以下のドキュメントは簡体字中国語のみです。

## 機能

**アカウントと計量**

- 管理者はアカウントの作成・編集・停止・削除ができ、有効期限、日次／月次のクォータ、QPS とバースト、デバイス数の上限を設定できます。アカウントを削除すると、そのユーザーのデータもすべて消去されます。
- デバイスごとに UUID の認証情報を発行し、専用 URL または `Authorization: Bearer` で接続します。認証情報はいつでもローテーション・失効できます。
- 使用量はサーバー全体、ユーザー、デバイスの 3 段階で正確に計量され、クォータの消費はクエリの受け付けと同じトランザクションで行われます。

**ユーザーが自分で変更できる設定**

- セキュリティとプライバシー：DNS リバインディング対策、クエリタイプ別のブロック（HTTPS／SVCB を含む）、ECS の除去、管理者が用意した脅威インテリジェンスリストのワンクリック有効化。
- カスタムルール：ブロック、許可、A／AAAA／CNAME の書き換え。完全一致、サフィックス、キーワード、正規表現でマッチできます。
- 公開ブロックリスト：管理者がカタログを管理し、各ユーザーが個別にオン／オフします。
- 応答の最適化：IPv4／IPv6 の優先、TTL の下限・上限、CNAME チェーンの平坦化、応答順のシャッフル、ECS アドレスの上書き。
- ワンクリックのセーフモードと、自分のポリシーをすべて一時停止する機能。
- 詳細なクエリログを残すかどうか、何時間残すかをユーザー自身が決められます。
- DNS Lookup：現在有効なポリシーチェーン全体で任意のドメインをテストできます。

**管理と運用**

- 制御データと統計データは bbolt（外部依存なし）または MySQL 5.7 / 8.4 に保存できます。
- 統計とクエリログで、各応答がアップストリーム、キャッシュ、ルール、公開リストのどれから来たかが分かります。
- 監査ログ、システムのヘルスモニタリング、認証付きの Prometheus `/metrics`。
- 管理対象の実行時設定：アップストリーム、キャッシュ、統計の保持期間を、パネルから検証・ホットリロード・ロールバックできます。変更が失敗しても、実行中の設定には影響しません。
- オフラインのバックアップと復元、bbolt から MySQL への移行、管理者がログインできなくなったときのためのオフライン `reset-password`。

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
| 設定 | [マルチユーザー設定](docs/service-config.md)、[ストレージと移行](docs/storage.md) |
| 運用 | [初期化、バックアップと復元、管理対象設定、パスワードのリセット](docs/operations.md)、[セキュリティと稼働監視](docs/monitoring.md) |
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
