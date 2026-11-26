package federation

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown renders the report as a self-contained Markdown document.
// Every externally derived value was sanitized during aggregation, and each
// one is emitted inside a code span so the document cannot gain markup or
// break out of its table cells.
func RenderMarkdown(report Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# 連合レポート: `%s`\n\n", report.TargetHost)
	fmt.Fprintf(&b, "- 対象サーバー: `%s`\n", report.TargetHost)
	fmt.Fprintf(&b, "- サーバーバージョン: `%s`\n", report.SelfVersion)
	fmt.Fprintf(&b, "- 生成日時 (UTC): `%s`\n", formatTimestamp(report.GeneratedAt))
	fmt.Fprintf(&b, "- 新規連合ウィンドウ: `%dh`\n", report.NewWindowHours)
	fmt.Fprintf(&b, "- アクティブ判定ウィンドウ: `%dd`\n\n", report.ActiveWindowDays)

	b.WriteString("## 1. 全体サマリ\n\n")
	b.WriteString("| 項目 | 件数 |\n")
	b.WriteString("| --- | ---: |\n")
	fmt.Fprintf(&b, "| 連合先総数 | %d |\n", report.Overview.TotalCount)
	fmt.Fprintf(&b, "| アクティブ | %d |\n", report.Overview.ActiveCount)
	fmt.Fprintf(&b, "| 応答なし | %d |\n", report.Overview.NotRespondingCount)
	fmt.Fprintf(&b, "| 停止 | %d |\n", report.Overview.SuspendedCount)
	fmt.Fprintf(&b, "| ブロック | %d |\n\n", report.Overview.BlockedCount)

	b.WriteString("## 2. ソフトウェア・バージョン別インスタンス一覧 (停止・ブロック除く)\n\n")
	b.WriteString("| ホスト | ソフトウェア | バージョン |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, instance := range report.SoftwareInstances {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n",
			instance.Host,
			instance.SoftwareName,
			instance.SoftwareVersion,
		)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "## 3. 新規連合 (直近%dh: %d件)\n\n", report.NewWindowHours, report.NewInstances.Total)
	if report.NewInstances.Total == 0 {
		b.WriteString("該当なし\n\n")
	} else {
		b.WriteString("| ホスト | ソフトウェア | バージョン | ユーザー数 | ノート数 | 初回取得 |\n")
		b.WriteString("| --- | --- | --- | ---: | ---: | --- |\n")
		for _, instance := range report.NewInstances.Items {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d | %d | `%s` |\n",
				instance.Host,
				instance.SoftwareName,
				instance.SoftwareVersion,
				instance.UsersCount,
				instance.NotesCount,
				formatTimestamp(instance.FirstRetrievedAt),
			)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(
		&b,
		"## 4. 要注意: 応答なし・未停止 (計%d件 / うちFF関係あり%d件)\n\n",
		report.UnhealthyInstances.UnhealthyTotal,
		report.UnhealthyInstances.UnhealthyWithFFCount,
	)
	if report.UnhealthyInstances.UnhealthyTotal == 0 {
		b.WriteString("該当なし\n\n")
	} else {
		b.WriteString("| ホスト | ソフトウェア | バージョン | FF数 | ユーザー数 | ノート数 | 最終受信 |\n")
		b.WriteString("| --- | --- | --- | ---: | ---: | ---: | --- |\n")
		for _, instance := range report.UnhealthyInstances.Items {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d | %d | %d | %s |\n",
				instance.Host,
				instance.SoftwareName,
				instance.SoftwareVersion,
				summedCount(instance.FollowingCount, instance.FollowersCount),
				instance.UsersCount,
				instance.NotesCount,
				formatOptionalTimestamp(instance.LatestRequestReceivedAt),
			)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 5. 連合規模 Top 10 (停止・ブロック除く / FF数順)\n\n")
	b.WriteString("| ホスト | ソフトウェア | バージョン | FF数 | ユーザー数 | ノート数 |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: |\n")
	for _, instance := range report.TopInstances {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d | %d | %d |\n",
			instance.Host,
			instance.SoftwareName,
			instance.SoftwareVersion,
			summedCount(instance.FollowingCount, instance.FollowersCount),
			instance.UsersCount,
			instance.NotesCount,
		)
	}

	return b.String()
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTimestamp(value *time.Time) string {
	if value == nil {
		return "`unknown`"
	}
	return "`" + formatTimestamp(*value) + "`"
}
