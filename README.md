# kinakomate

[Misskey](https://misskey-hub.net/) 系サービスの PostgreSQL バックアップを作成し、実際に復旧できることを検証する Go 製 CLI です。
`backup` は DB を gzip 圧縮 SQL として S3 に保存し、`restore-test` は検証専用 DB に復元して API からデータを確認します。同じイメージを使いますが、実行ジョブ・DB 接続先・認証情報は分離してください。

> **復元先の `misskey` データベースは毎回削除・再作成されます。本番ではなく、復元検証専用の環境で実行してください。**

## 実行

コンテナは `ghcr.io/azuki774/kinakomate:<短縮コミットSHA>` または `ghcr.io/azuki774/kinakomate:<Gitタグ>`。環境変数を注入し、コンテナの引数に `backup` または `restore-test` を指定します。PostgreSQL 18 系の `pg_dump` と `psql` を同梱しています。
設定一覧は [env.example](env.example)（必須項目・既定値・認証情報）を参照してください。ファイルの自動読み込みは行いません。

### backup

`DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASS` / `DB_NAME` と、`S3_REGION` / `S3_BUCKET` / `S3_KEY`、AWS 認証情報を設定してください。`S3_ENDPOINT` は任意です。Kubernetes・Misskey の設定や権限は不要です。

```sh
# 上記環境変数を実行環境に注入してから実行
kinakomate backup
```

処理は `pg_dump --format=plain --no-owner --no-acl` → gzip 一時ファイル → gzip 全体の検証 → S3 upload → HEAD のサイズ照合 → 一時ファイル削除の順です。ダンプ生成が失敗した場合はアップロードしません。SQL 全体をメモリに保持せず、圧縮済みダンプ分の一時ディスクが必要です。

- `S3_KEY` は拡張子まで含む完全なキーを指定し、既存 worker と同じ `.sql.gz` キーを維持できます。世代選択・削除は行いません。
- S3 は指定キーへの upload・multipart 中断・HEAD に必要な権限を付与してください。Versioning・保持期間・未完了 multipart の回収は保存先で設定します。
- DB ユーザーには対象 DB 全体を dump できる権限が必要です。RLS・large object を含む場合も欠落なく取得できることを確認してください。
- 成功は保存確認までを意味します。復元可能性は、別ジョブの `restore-test` で確認してください。
- upload の失敗では既に更新済みの場合もあります。自動で旧オブジェクトへ戻したり、保存先を削除したりはしません。
- CronJob は `concurrencyPolicy: Forbid`、`restartPolicy: Never`、`backoffLimit: 0` と、実測に基づく実行期限・リソース・一時 volume 容量を指定してください。別ジョブや手動実行も含め、同じキーの writer は1つに限定します。

### restore-test

実行前に用意するもの:

- 同じ namespace の web／DB workload（Deployment または StatefulSet）。`POD_NAMESPACE` を明示的に注入してください。未設定時は `default` を操作します。
- 実行用 ServiceAccount と RBAC。対象 namespace の `apps` API group の `deployments`／`statefulsets` に `get`／`update` を許可し、`resourceNames` を対象の2つの workload 名に限定します。
- S3 の固定オブジェクトを読み取れる AWS 認証情報と、gzip 圧縮されたプレーン SQL ダンプ。
- `postgres` DB への接続、対象 DB の所有権、`CREATEDB`、復元先テーブルなどの `ANALYZE` が可能な DB ユーザー。
- gzip ダンプを保存できる書き込み可能な一時領域（通常 `/tmp`）。

CronJob・RBAC・Secret などのデプロイ定義は別のインフラリポジトリで管理します。runner は Kubernetes API から Secret を取得しません。

## 復元検証の流れ

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
- stderr に JSON ログを出力します。`backup final report` は `preflight`／`dump`／`validate`／`upload`／`cleanup`、`restore-test final report` は `preflight`／`prepare`／`restore`／`verify`／`cleanup` の結果・所要時間をまとめます。
- バックアップでは gzip サイズ・保存確認済みかどうかも報告します。保存後の一時ファイル削除失敗は終了コード `1` ですが、保存確認済みの状態は保持します。
- DB パスワード・AWS 認証情報・Discord webhook URL はログに出しません。DB 接続先や S3 bucket/key は記録されるため、ログの扱いに注意してください。
- `DISCORD_NOTIFICATION_WEBHOOK` 設定時は結果・失敗フェーズ・gzip サイズ・処理時間を通知します。復元では復元直後の DB サイズ、バックアップでは保存確認状態も含みます。DB サイズ取得や通知だけの失敗は終了コードを変更しません。

## 既存 PostgreSQL worker からの移行

旧変数の別名対応はありません。インフラ側で `DB_PASSWORD` → `DB_PASS`、`BUCKET_NAME` → `S3_BUCKET`、`BUCKET_URL` → `S3_ENDPOINT`、`AWS_REGION` → `S3_REGION`、`DISCORD_WEBHOOK` → `DISCORD_NOTIFICATION_WEBHOOK` に注入先を変更します。`${BUCKET_DIR}/${BACKUP_NAME}.sql.gz` の既存値を `S3_KEY` に指定し、旧イメージの既定値に依存していた `DB_PORT` も明示します。`SKIP_S3_UPLOAD` はありません。

別キーへの試行と空 DB への復元、検証環境での API/GTL 確認を行ってから旧 writer を停止し、実行中ジョブがないことを確認して切り替えてください。時刻は Pod の `TZ` ではなく CronJob の `timeZone` と実際の実行時刻を確認して維持します。問題時は新 writer を停止し、旧イメージ・環境変数へ戻せますが、保存済みのオブジェクトは自動では戻りません。

## 開発

Go のバージョンは [go.mod](go.mod)、開発環境は [flake.nix](flake.nix) を参照してください。

```sh
make build          # bin/kinakomate を生成
./bin/kinakomate --help
make test
make vet
make lint           # golangci-lint が必要
make docker-build
make test-backup-integration # 実イメージ・PostgreSQL・S3 を使った結合テスト
# make docker-push  # コミットSHAタグで GHCR に push（認証が必要）
```

`master` の push 時は短縮コミット SHA タグで GHCR に公開します。Git タグを push すると、同じイメージを Git タグ名と短縮コミット SHA の両方で公開します。Git タグ名には Docker イメージタグとして有効な名前を使ってください。公開 workflow を含むコミットにタグを付け、workflow の成功後に利用します。

```sh
git tag 1.0.1
git push origin 1.0.1
# 公開完了後
docker pull ghcr.io/azuki774/kinakomate:1.0.1
```

ローカルでのビルド、ヘルプ表示、`backup` にクラスタは不要です。復元処理は Kubernetes の in-cluster 認証を使うため、kubeconfig でのローカル実行には対応していません。

`scripts/test-backup-integration.sh` は CI とローカルで共用する結合テスト専用スクリプトです。本番バックアップの実行には使いません。Docker、または `CONTAINER_ENGINE=podman make test-backup-integration` で Podman を使用し、テスト用コンテナ・ネットワークを作成して終了時に削除します。実運用の認証情報や保存先は使用しません。

[AI エージェント向けルール](AGENTS.md) · [Issue 一覧](https://github.com/azuki774/kinakomate/issues) · [MIT License](LICENSE)
