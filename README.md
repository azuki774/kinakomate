# kinakomate

[misskey](https://misskey-hub.net/) 系サービスの運用を支援するツール群のリポジトリです。

主な機能は、データベースのリストア復旧を検証する restore-test機能です。

## ドキュメント

- [AGENTS.md](AGENTS.md) — このリポジトリのAIエージェント向けルール
- [Issue 一覧](https://github.com/azuki774/kinakomate/issues) — 現在の実装計画

## 入力環境変数

`restore-test` は以下の環境変数を入力として受け取ります。`DB_PASS` と S3 認証情報はログ出力の対象外です。

| 変数 | 必須 | 説明 |
|---|---|---|
| `WORKLOAD` | yes | 対象となる Kubernetes workload 名（RFC 1123 label） |
| `S3_REGION` | yes | バックアップ bucket のリージョン |
| `S3_BUCKET` | yes | 固定バックアップ object を含む bucket |
| `S3_KEY` | yes | 固定バックアップ object の key（世代選択はしない固定 key のみ取得） |
| `S3_ENDPOINT` | no | S3-compatible endpoint。未設定なら AWS デフォルト endpoint、設定時は path-style でアクセス |
| `DB_HOST` | yes | 復元先 PostgreSQL の host |
| `DB_PORT` | yes | 復元先 PostgreSQL の port |
| `DB_USER` | yes | 復元先 PostgreSQL の user |
| `DB_PASS` | yes | 復元先 PostgreSQL の password（ログに出さない） |

## S3 認証情報の依存

S3 への read-only アクセスは、AWS SDK の標準 credential chain が環境変数
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`（必要に応じて
`AWS_SESSION_TOKEN`）から読み取る方式を採用しています。kinakomate 自体は
Kubernetes API から Secret を取得せず、配布・注入はインフラ定義側
（CronJob / manifest / Secret / Infisical）の別作業として管理します。

## ライセンス

[MIT License](LICENSE)
