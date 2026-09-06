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
| `WEB_WORKLOAD` | yes | スケール制御対象の web Kubernetes workload 名（RFC 1123 label） |
| `DB_WORKLOAD` | yes | スケール制御対象の db Kubernetes workload 名（RFC 1123 label） |
| `S3_REGION` | yes | バックアップ bucket のリージョン |
| `S3_BUCKET` | yes | 固定バックアップ object を含む bucket |
| `S3_KEY` | yes | 固定バックアップ object の key（世代選択はしない固定 key のみ取得） |
| `S3_ENDPOINT` | no | S3-compatible endpoint。未設定なら AWS デフォルト endpoint、設定時は path-style でアクセス |
| `DB_HOST` | yes | 復元先 PostgreSQL の host |
| `DB_PORT` | yes | 復元先 PostgreSQL の port |
| `DB_USER` | yes | 復元先 PostgreSQL の user |
| `DB_PASS` | yes | 復元先 PostgreSQL の password（ログに出さない） |
| `MISSKEY_BASE_URL` | yes | 復元確認対象の Misskey URL。`http` / `https` の host を含む origin（末尾の `/` は任意） |

復元先のデータベース名は固定値 `misskey` です（環境変数では指定しません）。
`MISSKEY_BASE_URL` には user/password、root 以外の path、query、fragment を含められません。

## 復元後の検証

リストア後に web を 1 replica で起動し、`GET /healthz` の成功を待ちます。
続いて `POST /api/notes/global-timeline` で最新の Note を 1〜10 件取得し、
復元データを API から参照できることを確認します。

## Kubernetes RBAC

Kubernetes の標準 ServiceAccount 権限では workload の参照や replica 数の変更は
できません。`restore-test` の実行用 ServiceAccount には、対象 workload と同じ
namespace で次の権限を追加してください。`resourceNames` は各環境の
`WEB_WORKLOAD` と `DB_WORKLOAD` の値に置き換えます。

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kinakomate-restore-test
rules:
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets"]
    resourceNames:
      - "<WEB_WORKLOAD の値>"
      - "<DB_WORKLOAD の値>"
    verbs: ["get", "update"]
```

この `Role` を実行用 ServiceAccount に割り当てる `RoleBinding` は、CronJob などと
同様にデプロイ先のインフラ定義で管理してください。runner は Kubernetes API から
Secret を取得しないため、`secrets` に対する権限は不要です。

## データベースの再作成（リストア前の初期化）

プレーン SQL のダンプには既存オブジェクトを消す `--clean` 相当の処理がないため、
runner はリストアの直前に必ず対象データベースを作り直します。接続先は
maintenance database の `postgres` で、次の順に実行します。

1. 対象データベースへの残存接続を `pg_terminate_backend` で切断
2. `DROP DATABASE IF EXISTS misskey;`
3. `CREATE DATABASE misskey TEMPLATE template0;`（`DB_USER` が owner になる）

その後に gzip ダンプを `psql --single-transaction --set ON_ERROR_STOP=1` で
流し込みます。この初期化は対象データベースを完全に置き換える破壊的操作のため、
runner を向けた環境では対象 DB 内のデータは常に失われます。

必要な権限: `DB_USER` は `postgres` maintenance database に接続でき、かつ
対象データベースの ownership と `CREATEDB` 権限を持つ必要があります。

## S3 認証情報の依存

S3 への read-only アクセスは、AWS SDK の標準 credential chain が環境変数
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`（必要に応じて
`AWS_SESSION_TOKEN`）から読み取る方式を採用しています。kinakomate 自体は
Kubernetes API から Secret を取得せず、配布・注入はインフラ定義側
（CronJob / manifest / Secret / Infisical）の別作業として管理します。

## ライセンス

[MIT License](LICENSE)
