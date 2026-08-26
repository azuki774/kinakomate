# AGENTS.md

このファイルは、このリポジトリでコードを書く AI エージェント（opencode 等）のために、プロジェクト固有のルールを記述するものです。

## プロジェクト概要

kinakomate は misskey 系サービスの運用ツール群を開発するリポジトリです。現在の主な成果物はデータベースのリストア復旧を定期的に検証するコンテナ（restore-test-runner）です。外部環境（バックアップ・システム構成・ワークロード名・namespace など）の詳細は、必要最小限に留め、機密情報やインフラ固有の識別子をこのリポジトリに記録しないでください。

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
  - 本リポジトリは復元・検証ロジック自体（runner）とそのビルド・CI・文書化を扱う。
  - デプロイ対象の作成・変更（CronJob・manifest・RBAC・Secret）、Kubernetes リソースの直接操作、S3 オブジェクトの作成・更新・削除、ネットワークの外部公開、通知送信、ブラウザ／スクリーンショット取得は **runner の責務外**。これらは別のインフラ定義リポジトリ側で管理する。
- **認証情報**: runner はデプロイ時に注入される外部ストレージへの read-only 認証情報（環境変数）のみを利用し、Kubernetes API から直接 Secret を取得しない。

## 検証コマンド

実装が進むにつれ、以下を追加・更新してください。

- `make lint` / `make test` / `make build` / `make docker-push`
- `golangci-lint` / `go vet` / `go test`
- `master` へのマージは CI（lint / vet / test）の必須チェックでブロックする。
