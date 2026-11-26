# kinakomate

[Misskey](https://misskey-hub.net/) 系サービスの PostgreSQL バックアップ・復元検証と、連合先の状況レポートを提供する Go 製 CLI です。
`backup` は DB を gzip 圧縮 SQL として S3 に保存し、`restore-test` は検証専用 DB に復元して API からデータを確認します。`federation-report` は Misskey の公開 API から連合先の状況を集計し、Markdown レポートとしてファイルに出力します。同じイメージを使いますが、実行ジョブ・DB 接続先・認証情報は分離してください。

> **復元先の `misskey` データベースは毎回削除・再作成されます。本番ではなく、復元検証専用の環境で実行してください。**

## 実行

コンテナは `ghcr.io/azuki774/kinakomate:<コミットSHA>`。環境変数を注入し、コンテナの引数に `backup`、`restore-test`、または `federation-report` を指定します。PostgreSQL 18 系の `pg_dump` と `psql` を同梱しています。
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

### federation-report

Misskey の公開 API だけを使い、連合先インスタンスの状況を Markdown レポートとしてファイルに出力します。DB・S3・Kubernetes・管理者 API トークンは不要で、Discord 通知も行いません。各設定の許容範囲は [env.example](env.example) を参照してください。

- **必須環境変数**
  - `MISSKEY_BASE_URL`: 対象 Misskey インスタンスの origin URL（`restore-test` と共通）
  - `FEDERATION_REPORT_OUTPUT_PATH`: 出力する Markdown ファイルのパス
- **オプション（任意）環境変数**
  - `MISSKEY_FEDERATION_NEW_WINDOW_HOURS`: `firstRetrievedAt` から新規連合先とみなす期間（既定 `168` 時間＝1週間）
  - `MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS`: `latestRequestReceivedAt` からアクティブとみなす期間（既定 `7` 日）
  - `MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS`: API リクエスト1回の制限時間（既定 `10` 秒）

```sh
MISSKEY_BASE_URL=https://misskey.example \
FEDERATION_REPORT_OUTPUT_PATH=/var/lib/kinakomate/federation-report.md \
kinakomate federation-report
```

処理は `preflight`（引数・環境変数・出力先の確認）→ `fetch`（`POST /api/meta` と `POST /api/federation/instances` の取得・集計）→ `write`（Markdown 出力）の順です。`/api/federation/instances` は `limit: 100`・`sort: "+firstRetrievedAt"` でページングし、`host` で重複を排除してメモリ上で集計します。リダイレクトは追従せず、1レスポンスあたり 10 MiB、ページ数は 500 を上限とします。

レポートは、対象サーバーと自サーバーのバージョン、全体サマリ（連合先総数・アクティブ数・応答なし数・停止数・ブロック数）、ソフトウェア・バージョン別のインスタンス一覧、新規連合（`notesCount` 順）、応答なしの要注意連合先（FF 関係あり優先）、FF 合計数上位の連合先で構成します（アクティブ数・ソフトウェア一覧・要注意連合先・FF 合計数上位はいずれも停止・ブロック中の連合先を除きます）。

連合先が自己申告する情報は非信頼データとして扱い、`name`・`description`・連絡先・アイコン URL などの自由文は取得対象から除外します。レポートに出力する `host`・`softwareName`・`softwareVersion` も、許可された ASCII 文字以外を置換し、長さ上限を超える値は末尾を `...` に省略（空値は `unknown`）したうえでインラインコードとして出力するため、Markdown リンクや HTML として解釈されません。

出力先と同じディレクトリに一時ファイルを作成して書き込み、成功後に rename することで、既存ファイルを原子的に置き換えます。ファイル権限は `0644` です。途中で失敗しても不完全な内容は残らないため、親ディレクトリだけ事前に用意してください。レポートには連合先の host など外部由来の情報が含まれるため、保存先と配布範囲に注意してください。

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

- 終了コードは成功時 `0`、入力・バックアップ・復元・連合レポート・後処理などの失敗時 `1`。
- stderr に JSON ログを出力します。`backup final report` は `preflight`／`dump`／`validate`／`upload`／`cleanup`、`restore-test final report` は `preflight`／`prepare`／`restore`／`verify`／`cleanup`、`federation-report final report` は `preflight`／`fetch`／`write` の結果・所要時間をまとめます。連合レポートの最終ログには集計件数も含みます。
- バックアップでは gzip サイズ・保存確認済みかどうかも報告します。保存後の一時ファイル削除失敗は終了コード `1` ですが、保存確認済みの状態は保持します。
- DB パスワード・AWS 認証情報・Discord webhook URL はログに出しません。DB 接続先や S3 bucket/key は記録されるため、ログの扱いに注意してください。
- `DISCORD_NOTIFICATION_WEBHOOK` 設定時は結果・失敗フェーズ・gzip サイズ・処理時間を通知します。バックアップは Embed カードに DB 名・保存ファイル名（`S3_KEY` の末尾）・`s3://bucket/key`・保存確認状態・実行完了日時も表示します。成功は緑、保存確認前の失敗は赤、保存確認済みで後処理などが失敗した場合は黄です。設定取得前の失敗では対象情報を「未取得」と表示し、特殊文字は表示用にエスケープ、長い値は末尾の `…` で省略を示します。
- バックアップの DB 名と S3 bucket/key は Discord にも送信されるため、通知先の閲覧権限に注意してください。復元通知はテキスト形式のまま、復元直後の DB サイズも含みます。DB サイズ取得や通知だけの失敗は終了コードを変更しません。`federation-report` は Discord 通知を行いません。

## 既存 PostgreSQL worker からの移行

旧変数の別名対応はありません。インフラ側で `DB_PASSWORD` → `DB_PASS`、`BUCKET_NAME` → `S3_BUCKET`、`BUCKET_URL` → `S3_ENDPOINT`、`AWS_REGION` → `S3_REGION`、`DISCORD_WEBHOOK` → `DISCORD_NOTIFICATION_WEBHOOK` に注入先を変更します。`${BUCKET_DIR}/${BACKUP_NAME}.sql.gz` の既存値を `S3_KEY` に指定し、旧イメージの既定値に依存していた `DB_PORT` も明示します。`SKIP_S3_UPLOAD` はありません。

別キーへの試行と空 DB への復元、検証環境での API/GTL 確認を行ってから旧 writer を停止し、実行中ジョブがないことを確認して切り替えてください。時刻は Pod の `TZ` ではなく CronJob の `timeZone` と実際の実行時刻を確認して維持します。問題時は新 writer を停止し、旧イメージ・環境変数へ戻せますが、保存済みのオブジェクトは自動では戻りません。

## 開発

Go のバージョンは [go.mod](go.mod)、開発環境は [mise.toml](mise.toml) で管理します。mise を導入し、リポジトリ直下でツールをインストールしてください。

```sh
mise install
```

`mise activate` をシェルに設定していない場合は、`mise exec -- make ...` の形式で実行します。

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

ローカルでのビルド、ヘルプ表示、`backup` や `federation-report` にクラスタは不要です。復元処理は Kubernetes の in-cluster 認証を使うため、kubeconfig でのローカル実行には対応していません。

`scripts/test-backup-integration.sh` は CI とローカルで共用する結合テスト専用スクリプトです。本番バックアップの実行には使いません。Docker、または `CONTAINER_ENGINE=podman make test-backup-integration` で Podman を使用し、テスト用コンテナ・ネットワークを作成して終了時に削除します。実運用の認証情報や保存先は使用しません。

[AI エージェント向けルール](AGENTS.md) · [Issue 一覧](https://github.com/azuki774/kinakomate/issues) · [MIT License](LICENSE)
