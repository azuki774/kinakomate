# kinakomate

kinakomate（きなこメイト）は、[misskey](https://misskey-hub.net/) 系サービスの運用を支援するツール群のリポジトリです。

現在の主な対象は、データベースのリストア復旧を定期的に検証する **restore-test-runner** コンテナです。本リポジトリはその復元・検証ロジックと、ビルド・CI・入力契約の文書化を扱います。

## プロジェクトの目的

このリポジトリの成果物は「運用ツール」であり、データベースの **リストア復旧テスト** を中心とした、定期的なリカバリー経路の検証を自動化するものです。

単にバックアップファイルの存在を確認するだけでは不十分です。kinakomate は以下の一連の流れを通して、実際にリストア・復旧できることを継続的に検証します。

1. バックアップダンプ（gzip 圧縮された plain SQL）の実在を確認する
2. 空のデータベースへダンプを復元する
3. アプリケーションを起動し、migration を実行する
4. 復元後の API が利用可能になること（readiness）を確認する
5. 復元済みデータに対する検証コマンド（checks）を実行する

検証成功後も復元済みの QA workload は起動したまま保持し、目視確認や将来のブラウザ試験の土台とします。

## リポジトリ構成

現在は実装初期段階です。成果物は次の形で整備される想定です。

```
cmd/              # main パッケージ（エントリポイント）
  kinakomate/    # CLI エントリ、サブコマンドのディスパッチ
internal/         # 内部実装
  config/         # 入力設定の契約と pre-flight validation
  cluster/        # Kubernetes workload の scale / readiness 制御
  s3/             # S3 ダンプのストリーム取得
  restore/        # PostgreSQL への復元（restore-test サブコマンド）
  checks/         # 順序付き検証コマンドの実行
  log/            # 構造化ログ
Dockerfile        # マルチステージ、distroless runtime イメージ
Makefile          # build / test / lint / docker-push
.golangci.yml     # golangci-lint 設定
.github/workflows # CI（lint / vet / test）
README.md
AGENTS.md
```

> 上記のうち `cmd/kinakomate`・`internal/restore`・`internal/log` は現在実装済み（scaffold）です。それ以外は以降の Issue で整備します。

## 決定事項

本 Issue（#2）で確定した実装方針は以下の通りです。

| 項目 | 決定 |
| --- | --- |
| 実装言語 | Go（単一バイナリ CLI） |
| コンテナイメージ | マルチステージビルド、distroless を runtime に使用 |
| イメージリポジトリ | `ghcr.io/azuki774/kinakomate` |
| レジストリ | `ghcr.io` |
| Linter | `golangci-lint` |
| サブコマンド | `restore-test`（リストア検証テスト）を提供 |

イメージは `git rev-parse --short HEAD` 由来のコミット SHA を tag として `ghcr.io/azuki774/kinakomate` へ push します（`make docker-push`）。

## ドキュメント

- [AGENTS.md](AGENTS.md) — このリポジトリの開発者向け運用ルール
- [Issue 一覧](https://github.com/azuki774/kinakomate/issues) — 現在の実装計画

## ライセンス

[MIT License](LICENSE)
