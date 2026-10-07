# kinakomate

[Misskey](https://misskey-hub.net/) 系サービスのバックアップから、実際に復旧できることを検証する Go 製 CLI です。
`restore-test` は S3 の gzip 圧縮 SQL ダンプを PostgreSQL に復元し、web 起動後に API からデータを確認します。

> **復元先の `misskey` データベースは毎回削除・再作成されます。本番ではなく、復元検証専用の環境で実行してください。**

## 実行

コンテナは `ghcr.io/azuki774/kinakomate:<コミットSHA>`。Kubernetes 内で環境変数を注入し、コンテナの引数に `restore-test` を指定します。
設定一覧は [env.example](env.example)（必須項目・既定値・認証情報）を参照してください。ファイルの自動読み込みは行いません。

実行前に用意するもの:

- 同じ namespace の web／DB workload（Deployment または StatefulSet）。`POD_NAMESPACE` を明示的に注入してください。未設定時は `default` を操作します。
- 実行用 ServiceAccount と RBAC。対象 namespace の `apps` API group の `deployments`／`statefulsets` に `get`／`update` を許可し、`resourceNames` を対象の2つの workload 名に限定します。
- S3 の固定オブジェクトを読み取れる AWS 認証情報と、gzip 圧縮されたプレーン SQL ダンプ。
- `postgres` DB への接続、対象 DB の所有権、`CREATEDB`、復元先テーブルなどの `ANALYZE` が可能な DB ユーザー。
- gzip ダンプを保存できる書き込み可能な一時領域（通常 `/tmp`）。コンテナには `psql` を同梱しています。

CronJob・RBAC・Secret などのデプロイ定義は別のインフラリポジトリで管理します。runner は Kubernetes API から Secret を取得しません。

## 検証の流れ

1. 設定・接続を確認し、S3 の固定キーからダンプを取得して gzip を検証。
2. web を 0 replica、DB を 1 replica にし、停止・起動を待って DB 接続を確認。
3. 既存接続を切断し、`misskey` DB を `template0` から再作成。SQL を単一トランザクションで復元し、エラー時は中断。
4. web 起動前に `ANALYZE` で統計情報を更新。タイムアウトや警告を含む診断出力も失敗として扱います。
5. web を 1 replica にし、`GET /healthz` の成功と `POST /api/notes/global-timeline`（GTL）で Note を1〜10件取得できることを確認。
6. 成功時は web／DB をともに 0 replica に設定。一時ファイルを削除。

GTL の DB timeout・HTTP 5xx・通信エラーは再試行します。HTTP 4xx／3xx や不正なレスポンスは再試行しません。時間設定は [env.example](env.example) を参照してください。
初期化後の失敗時は web を 0 replica にする処理を試み、DB の停止処理は行いません。元の replica 数には戻しません。

## 結果の確認

- 終了コードは成功時 `0`、入力・復元・検証・後処理などの失敗時 `1`。
- stderr に JSON ログを出力し、`restore-test final report` に `preflight`／`prepare`／`restore`／`verify`／`cleanup` の結果・所要時間をまとめます。
- DB パスワード・AWS 認証情報・Discord webhook URL はログに出しません。DB 接続先や S3 bucket/key は記録されるため、ログの扱いに注意してください。
- `DISCORD_NOTIFICATION_WEBHOOK` 設定時は、成功／失敗・失敗フェーズ・復元直後の DB サイズ・gzip バックアップサイズ・処理時間を通知します。DB サイズ取得や通知だけの失敗は終了コードを変更しません。

## 開発

Go のバージョンは [go.mod](go.mod)、開発環境は [flake.nix](flake.nix) を参照してください。

```sh
make build          # bin/kinakomate を生成
./bin/kinakomate --help
make test
make vet
make lint           # golangci-lint が必要
make docker-build
# make docker-push  # コミットSHAタグで GHCR に push（認証が必要）
```

ローカルでのビルドやヘルプ表示にクラスタは不要です。復元処理は Kubernetes の in-cluster 認証を使うため、kubeconfig でのローカル実行には対応していません。

[AI エージェント向けルール](AGENTS.md) · [Issue 一覧](https://github.com/azuki774/kinakomate/issues) · [MIT License](LICENSE)
