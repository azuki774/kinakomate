package federation

import (
	"math"
	"net/url"
	"sort"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

const (
	// MaxListedTopInstances bounds the federation-size ranking.
	MaxListedTopInstances = 10
)

// Report is the normalized, self-contained snapshot rendered to Markdown.
type Report struct {
	TargetHost       string
	SelfVersion      string
	GeneratedAt      time.Time
	NewWindow        time.Duration
	NewWindowHours   int
	ActiveWindow     time.Duration
	ActiveWindowDays int

	Overview           Overview
	SoftwareInstances  []SoftwareInstance
	NewInstances       NewInstances
	UnhealthyInstances UnhealthyInstances
	TopInstances       []TopInstance
}

// Overview is the federation summary table.
type Overview struct {
	TotalCount         int
	ActiveCount        int
	NotRespondingCount int
	SuspendedCount     int
	BlockedCount       int
}

// SoftwareInstance maps one instance to the software and version it reports.
// Suspended and blocked instances are excluded.
type SoftwareInstance struct {
	Host            string
	SoftwareName    string
	SoftwareVersion string
}

// NewInstances describes instances first seen within the new window.
type NewInstances struct {
	Total int
	Items []NewInstance
}

// NewInstance is one instance listed in the new-instance section.
type NewInstance struct {
	Host             string
	SoftwareName     string
	SoftwareVersion  string
	UsersCount       int64
	NotesCount       int64
	FirstRetrievedAt time.Time
}

// UnhealthyInstances describes non-suspended, non-blocked instances that did
// not respond.
type UnhealthyInstances struct {
	UnhealthyTotal       int
	UnhealthyWithFFCount int
	Items                []UnhealthyInstance
}

// UnhealthyInstance is one instance listed in the unhealthy section.
type UnhealthyInstance struct {
	Host                    string
	SoftwareName            string
	SoftwareVersion         string
	UsersCount              int64
	NotesCount              int64
	FollowingCount          int64
	FollowersCount          int64
	LatestRequestReceivedAt *time.Time
}

// TopInstance is one entry in the federation-size ranking.
type TopInstance struct {
	Host            string
	SoftwareName    string
	SoftwareVersion string
	UsersCount      int64
	NotesCount      int64
	FollowingCount  int64
	FollowersCount  int64
}

type normalizedInstance struct {
	host                    string
	softwareName            string
	softwareVersion         string
	usersCount              int64
	notesCount              int64
	followingCount          int64
	followersCount          int64
	isNotResponding         bool
	isSuspended             bool
	isBlocked               bool
	firstRetrievedAt        *time.Time
	latestRequestReceivedAt *time.Time
}

// Aggregate normalizes and aggregates one snapshot. It never trusts the remote
// side: every host, software name, and version is sanitized, and negative
// counters are clamped to zero.
func Aggregate(now time.Time, cfg *config.FederationReportConfig, meta MetaResponse, instances []InstanceRecord) Report {
	var targetHost string
	var newWindow time.Duration
	var newWindowHours int
	var activeWindow time.Duration
	var activeWindowDays int
	if cfg != nil {
		if parsed, err := url.Parse(cfg.MisskeyBaseURL); err == nil {
			targetHost = parsed.Host
		}
		newWindow = cfg.NewWindow
		newWindowHours = cfg.NewWindowHours
		activeWindow = cfg.ActiveWindow
		activeWindowDays = cfg.ActiveWindowDays
	}

	selfVersion := meta.Version
	report := Report{
		TargetHost:       SanitizeHost(targetHost),
		SelfVersion:      SanitizeSoftwareVersion(&selfVersion),
		GeneratedAt:      now.UTC(),
		NewWindow:        newWindow,
		NewWindowHours:   newWindowHours,
		ActiveWindow:     activeWindow,
		ActiveWindowDays: activeWindowDays,
		Overview:         Overview{TotalCount: len(instances)},
	}

	normalized := make([]normalizedInstance, 0, len(instances))
	for _, record := range instances {
		instance := normalizedInstance{
			host:                    SanitizeHost(record.Host),
			softwareName:            SanitizeSoftwareName(record.SoftwareName),
			softwareVersion:         SanitizeSoftwareVersion(record.SoftwareVersion),
			usersCount:              clampCount(record.UsersCount),
			notesCount:              clampCount(record.NotesCount),
			followingCount:          clampCount(record.FollowingCount),
			followersCount:          clampCount(record.FollowersCount),
			isNotResponding:         record.IsNotResponding,
			isSuspended:             record.IsSuspended,
			isBlocked:               record.IsBlocked,
			firstRetrievedAt:        record.FirstRetrievedAt,
			latestRequestReceivedAt: record.LatestRequestReceivedAt,
		}
		normalized = append(normalized, instance)

		if instance.isNotResponding {
			report.Overview.NotRespondingCount++
		}
		if instance.isSuspended {
			report.Overview.SuspendedCount++
		}
		if instance.isBlocked {
			report.Overview.BlockedCount++
		}
		if !instance.isBlocked && !instance.isSuspended &&
			instance.latestRequestReceivedAt != nil &&
			now.Sub(*instance.latestRequestReceivedAt) <= activeWindow {
			report.Overview.ActiveCount++
		}
	}

	report.SoftwareInstances = aggregateSoftware(normalized)
	report.NewInstances = aggregateNewInstances(now, newWindow, normalized)
	report.UnhealthyInstances = aggregateUnhealthy(normalized)
	report.TopInstances = aggregateTop(normalized)
	return report
}

func aggregateSoftware(instances []normalizedInstance) []SoftwareInstance {
	listed := make([]SoftwareInstance, 0, len(instances))
	for _, instance := range instances {
		if instance.isBlocked || instance.isSuspended {
			continue
		}
		listed = append(listed, SoftwareInstance{
			Host:            instance.host,
			SoftwareName:    instance.softwareName,
			SoftwareVersion: instance.softwareVersion,
		})
	}

	sort.Slice(listed, func(i, j int) bool {
		if listed[i].SoftwareName != listed[j].SoftwareName {
			return listed[i].SoftwareName < listed[j].SoftwareName
		}
		if listed[i].SoftwareVersion != listed[j].SoftwareVersion {
			return listed[i].SoftwareVersion < listed[j].SoftwareVersion
		}
		return listed[i].Host < listed[j].Host
	})
	return listed
}

func aggregateNewInstances(now time.Time, window time.Duration, instances []normalizedInstance) NewInstances {
	candidates := make([]NewInstance, 0, len(instances))
	for _, instance := range instances {
		if instance.firstRetrievedAt == nil || now.Sub(*instance.firstRetrievedAt) > window {
			continue
		}
		candidates = append(candidates, NewInstance{
			Host:             instance.host,
			SoftwareName:     instance.softwareName,
			SoftwareVersion:  instance.softwareVersion,
			UsersCount:       instance.usersCount,
			NotesCount:       instance.notesCount,
			FirstRetrievedAt: *instance.firstRetrievedAt,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NotesCount != candidates[j].NotesCount {
			return candidates[i].NotesCount > candidates[j].NotesCount
		}
		if candidates[i].UsersCount != candidates[j].UsersCount {
			return candidates[i].UsersCount > candidates[j].UsersCount
		}
		if !candidates[i].FirstRetrievedAt.Equal(candidates[j].FirstRetrievedAt) {
			return candidates[i].FirstRetrievedAt.After(candidates[j].FirstRetrievedAt)
		}
		return candidates[i].Host < candidates[j].Host
	})

	return NewInstances{Total: len(candidates), Items: candidates}
}

type unhealthyCandidate struct {
	instance UnhealthyInstance
	hasFF    bool
	ffTotal  int64
}

func aggregateUnhealthy(instances []normalizedInstance) UnhealthyInstances {
	candidates := make([]unhealthyCandidate, 0, len(instances))
	withFF := 0
	for _, instance := range instances {
		if !instance.isNotResponding || instance.isSuspended || instance.isBlocked {
			continue
		}
		ffTotal := summedCount(instance.followingCount, instance.followersCount)
		hasFF := ffTotal > 0
		if hasFF {
			withFF++
		}
		candidates = append(candidates, unhealthyCandidate{
			instance: UnhealthyInstance{
				Host:                    instance.host,
				SoftwareName:            instance.softwareName,
				SoftwareVersion:         instance.softwareVersion,
				UsersCount:              instance.usersCount,
				NotesCount:              instance.notesCount,
				FollowingCount:          instance.followingCount,
				FollowersCount:          instance.followersCount,
				LatestRequestReceivedAt: instance.latestRequestReceivedAt,
			},
			hasFF:   hasFF,
			ffTotal: ffTotal,
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].hasFF != candidates[j].hasFF {
			return candidates[i].hasFF
		}
		if candidates[i].ffTotal != candidates[j].ffTotal {
			return candidates[i].ffTotal > candidates[j].ffTotal
		}
		left := candidates[i].instance.LatestRequestReceivedAt
		right := candidates[j].instance.LatestRequestReceivedAt
		switch {
		case left == nil && right != nil:
			return true
		case left != nil && right == nil:
			return false
		case left != nil && right != nil && !left.Equal(*right):
			return left.Before(*right)
		}
		return candidates[i].instance.Host < candidates[j].instance.Host
	})

	items := make([]UnhealthyInstance, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.instance)
	}
	return UnhealthyInstances{UnhealthyTotal: len(candidates), UnhealthyWithFFCount: withFF, Items: items}
}

type topCandidate struct {
	instance TopInstance
	ffTotal  int64
}

func aggregateTop(instances []normalizedInstance) []TopInstance {
	candidates := make([]topCandidate, 0, len(instances))
	for _, instance := range instances {
		if instance.isBlocked || instance.isSuspended {
			continue
		}
		candidates = append(candidates, topCandidate{
			instance: TopInstance{
				Host:            instance.host,
				SoftwareName:    instance.softwareName,
				SoftwareVersion: instance.softwareVersion,
				UsersCount:      instance.usersCount,
				NotesCount:      instance.notesCount,
				FollowingCount:  instance.followingCount,
				FollowersCount:  instance.followersCount,
			},
			ffTotal: summedCount(instance.followingCount, instance.followersCount),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ffTotal != candidates[j].ffTotal {
			return candidates[i].ffTotal > candidates[j].ffTotal
		}
		if candidates[i].instance.NotesCount != candidates[j].instance.NotesCount {
			return candidates[i].instance.NotesCount > candidates[j].instance.NotesCount
		}
		if candidates[i].instance.UsersCount != candidates[j].instance.UsersCount {
			return candidates[i].instance.UsersCount > candidates[j].instance.UsersCount
		}
		return candidates[i].instance.Host < candidates[j].instance.Host
	})

	items := make([]TopInstance, 0, min(len(candidates), MaxListedTopInstances))
	for _, candidate := range candidates[:min(len(candidates), MaxListedTopInstances)] {
		items = append(items, candidate.instance)
	}
	return items
}

func clampCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// summedCount adds two non-negative counters without overflowing on hostile
// input.
func summedCount(left, right int64) int64 {
	left = clampCount(left)
	right = clampCount(right)
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}
