# AGENTS.md

このファイルは、このリポジトリでコードを書く AI エージェント（opencode 等）のために、プロジェクト固有のルールを記述するものです。

## プロジェクト概要

kinakomate は misskey 系サービスの運用ツール群を開発するリポジトリです。PostgreSQL バックアップを作成する `backup` と、空データベースへのリストア復旧を検証する `restore-test` を同じ CLI・コンテナで提供します。外部環境（バックアップ・システム構成・ワークロード名・namespace など）の詳細は、必要最小限に留め、機密情報やインフラ固有の識別子をこのリポジトリに記録しないでください。

詳細は [README.md](README.md) を参照してください。

## 重要ルール（従うこと）

- **Git worktree** を利用して作業する。作業場所は `{repository_root}/.worktrees/{branch_name}`。
- **commit は Conventional Commits** の形式に従う（例: `feat:`, `fix:`, `docs:`, `chore:`）。
- `master` / `main` ブランチへの直接コミット・直接マージは禁止。
- コミット時は `git status`, `git diff`, `git log --oneline -10` を確認し、意図したファイルのみ stage する。秘密情報をコミットしない。
- これはシンプルな「バックアップ実在確認」ではなく、空データベースへの復元・起動・migration・API readiness・checks までを含む「実際に復旧できること」の検証を目的とする。

## 実装方針

- **実装言語**: Go（単一バイナリ CLI）。
- **コンテナイメージ**: マルチステージビルドと distroless な runtime イメージを使用する。
- **イメージリポジトリ / レジストリ**: `ghcr.io/azuki774/kinakomate`（`ghcr.io`）。`make docker-push` でコミット SHA を tag として push する。
- **Linter**: `golangci-lint` を使用する。
- **スコープ**:
  - 本リポジトリはバックアップ作成・復元検証ロジック自体と、そのビルド・CI・文書化を扱う。
  - `backup` は指定 DB の論理ダンプ作成と指定 S3 キーへの保存・保存確認を担当する。DB 初期化や Kubernetes API 操作は行わない。`restore-test` は検証専用環境での復元・workload 制御・API 検証を担当する。両コマンドの任意の Discord 結果通知は runner の責務に含む。
  - デプロイ定義（CronJob・manifest・RBAC・Secret）の作成・変更、S3 の世代保持・オブジェクト削除、ネットワークの外部公開、ブラウザ／スクリーンショット取得は責務外とし、別のインフラ定義リポジトリ側で管理する。
- **認証情報**: デプロイ時に環境変数で注入し、Kubernetes API から直接 Secret を取得しない。バックアップ元 DB・S3 書き込み用の認証情報と、復元先 DB・S3 read-only 用の認証情報は分離する。Discord webhook URL も秘密情報として扱う。

## 検証コマンド

実装が進むにつれ、以下を追加・更新してください。

- `make lint` / `make test` / `make build` / `make docker-push`
- `golangci-lint` / `go vet` / `go test`
- `master` へのマージは CI（lint / vet / test）の必須チェックでブロックする。
