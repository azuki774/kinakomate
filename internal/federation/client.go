package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

const (
	// instancesPageLimit is the page size requested from the Misskey API.
	instancesPageLimit = 100
	// maxPages bounds pagination so a misbehaving remote cannot make the
	// report run forever.
	maxPages = 500
	// maxResponseBytes bounds how much of a response body is read.
	maxResponseBytes = 10 << 20
)

var (
	errClientConfiguration = errors.New("federation client configuration is invalid")
	errRequestFailed       = errors.New("federation request failed")
	errResponseStatus      = errors.New("federation response status is not successful")
	errResponseTooLarge    = errors.New("federation response exceeds size limit")
	errResponseDecode      = errors.New("federation response could not be decoded")
	errPaginationLimit     = errors.New("federation instance pagination limit reached")
)

// MetaResponse carries the only self-metadata the report needs.
type MetaResponse struct {
	Version string `json:"version"`
}

// InstanceRecord is the deliberately narrow view of a federated instance.
// Free-text fields such as name, description, maintainer*, and iconUrl are
// omitted so a hostile remote value is discarded during decoding.
type InstanceRecord struct {
	ID                      string     `json:"id"`
	Host                    string     `json:"host"`
	UsersCount              int64      `json:"usersCount"`
	NotesCount              int64      `json:"notesCount"`
	FollowingCount          int64      `json:"followingCount"`
	FollowersCount          int64      `json:"followersCount"`
	IsNotResponding         bool       `json:"isNotResponding"`
	IsSuspended             bool       `json:"isSuspended"`
	IsBlocked               bool       `json:"isBlocked"`
	SoftwareName            *string    `json:"softwareName"`
	SoftwareVersion         *string    `json:"softwareVersion"`
	FirstRetrievedAt        *time.Time `json:"firstRetrievedAt"`
	LatestRequestReceivedAt *time.Time `json:"latestRequestReceivedAt"`
	LatestStatus            *int       `json:"latestStatus"`
}

// Client fetches the data needed for one federation report.
type Client interface {
	FetchMeta(ctx context.Context, cfg *config.FederationReportConfig) (MetaResponse, error)
	FetchInstances(ctx context.Context, cfg *config.FederationReportConfig) ([]InstanceRecord, error)
}

type httpClient struct {
	client *http.Client
}

// NewHTTPClient returns a client that never follows redirects so a hostile
// target cannot bounce requests to an unrelated host.
func NewHTTPClient() Client {
	return &httpClient{client: &http.Client{CheckRedirect: rejectRedirects}}
}

func rejectRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

type metaRequest struct {
	Detail bool `json:"detail"`
}

type instancesRequest struct {
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Sort   string `json:"sort"`
}

func (c *httpClient) FetchMeta(ctx context.Context, cfg *config.FederationReportConfig) (MetaResponse, error) {
	var meta MetaResponse
	if err := c.post(ctx, cfg, "/api/meta", metaRequest{Detail: false}, &meta); err != nil {
		return MetaResponse{}, err
	}
	return meta, nil
}

func (c *httpClient) FetchInstances(ctx context.Context, cfg *config.FederationReportConfig) ([]InstanceRecord, error) {
	// The host key maps to the record index so a repeated host replaces the
	// earlier record while keeping a stable output order.
	seen := make(map[string]int)
	records := make([]InstanceRecord, 0, instancesPageLimit)

	for page := range maxPages {
		var batch []InstanceRecord
		request := instancesRequest{
			Limit:  instancesPageLimit,
			Offset: page * instancesPageLimit,
			Sort:   "+firstRetrievedAt",
		}
		if err := c.post(ctx, cfg, "/api/federation/instances", request, &batch); err != nil {
			return nil, err
		}

		for _, record := range batch {
			key := strings.ToLower(strings.TrimSpace(record.Host))
			if key == "" {
				continue
			}
			if index, ok := seen[key]; ok {
				records[index] = record
				continue
			}
			seen[key] = len(records)
			records = append(records, record)
		}

		if len(batch) < instancesPageLimit {
			return records, nil
		}
	}

	return nil, errPaginationLimit
}

func (c *httpClient) post(ctx context.Context, cfg *config.FederationReportConfig, endpoint string, payload any, out any) error {
	if cfg == nil || strings.TrimSpace(cfg.MisskeyBaseURL) == "" {
		return errClientConfiguration
	}

	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = config.DefaultFederationRequestTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return errRequestFailed
	}

	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		strings.TrimRight(cfg.MisskeyBaseURL, "/")+endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return errRequestFailed
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient().Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		// The transport error can carry the target URL; never propagate it.
		return errRequestFailed
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errResponseStatus
	}

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errRequestFailed
	}
	if len(responseBody) > maxResponseBytes {
		return errResponseTooLarge
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return errResponseDecode
	}
	return nil
}

func (c *httpClient) httpClient() *http.Client {
	if c != nil && c.client != nil {
		return c.client
	}
	return &http.Client{CheckRedirect: rejectRedirects}
}
