package civitai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file adds a small, self-contained client for the CivitAI Apps catalog
// (GET /api/v1/apps). It is deliberately NOT part of the SDK-backed Reader
// interface: apps are a different shape from ModelListItem (no modelVersions[],
// no Raw showcase gallery), and the SDK exposes no app-listing call, so this
// speaks the REST contract directly.
//
// Reality note (2026-07): the catalog is DARK for a normal user — the endpoint
// returns {"items":[]} until CivitAI opens the marketplace launch gate. This
// client therefore treats an empty items list as a valid (not error) response;
// the caller renders an honest empty state.

// App mirrors the ListingCard DTO the /api/v1/apps endpoint returns. Only the
// fields the browse card renders are decoded; unknown fields are ignored so a
// forward-compatible payload never breaks the decode. kindData is a union — the
// onsite fields (liveUrl) OR the offsite fields (externalUrl/subKind) are
// populated per Kind.
type App struct {
	// ID is the app-listing id — a ULID-style STRING (e.g. "apl_01KYAZSZ…"),
	// NOT an int (verified live against /api/v1/apps). Creator.id IS an int.
	ID            string       `json:"id"`
	Slug          string       `json:"slug"`
	Kind          string       `json:"kind"` // "onsite" | "offsite"
	Name          string       `json:"name"`
	Tagline       string       `json:"tagline"`
	Category      string       `json:"category"`
	ContentRating string       `json:"contentRating"`
	IconURL       string       `json:"iconUrl"`
	CoverURL      string       `json:"coverUrl"`
	Creator       AppCreator   `json:"creator"`
	Recommend     AppRecommend `json:"recommend"`
	ReviewCount   int          `json:"reviewCount"`
	KindData      AppKindData  `json:"kindData"`
}

// AppCreator is the app author chip.
type AppCreator struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Image    string `json:"image"`
}

// AppRecommend carries the recommend tallies. RecommendPct is nullable in the
// API (no reviews yet), so it is a pointer.
type AppRecommend struct {
	RecommendedCount    int      `json:"recommendedCount"`
	NotRecommendedCount int      `json:"notRecommendedCount"`
	RecommendPct        *float64 `json:"recommendPct"`
}

// AppKindData is the union of the onsite + offsite kindData shapes. Only the
// fields for the item's Kind are meaningful. appBlockId/hasPage are intentionally
// omitted (unused by the card, and appBlockId's JSON type is not load-bearing
// here — omitting it keeps the decode tolerant).
type AppKindData struct {
	// onsite: the standalone, already-public block origin (may be empty).
	LiveURL string `json:"liveUrl"`
	// offsite: the external target and its sub-kind.
	SubKind     string `json:"subKind"`
	ExternalURL string `json:"externalUrl"`
}

// AppsMetadata is the keyset-cursor pagination envelope. NextCursor is the
// opaque token to pass as the next request's cursor; empty means no more pages.
type AppsMetadata struct {
	NextCursor string `json:"nextCursor"`
	NextPage   string `json:"nextPage"`
}

// AppsPage is the decoded {items, metadata} response.
type AppsPage struct {
	Items    []App        `json:"items"`
	Metadata AppsMetadata `json:"metadata"`
}

// AppsParams are the filter/sort/paging axes the endpoint supports. There is no
// free-text query axis — this is a filtered, sorted, paginated browse. Empty
// fields are omitted from the request.
type AppsParams struct {
	Kind     string // all | onsite | offsite
	Category string
	Sort     string // top-rated | popular | newest | name
	Cursor   string // opaque keyset cursor
	Limit    int    // 1..50
}

// AppsError is the typed error returned for a non-2xx catalog response. It
// carries the status so a caller can classify (e.g. a rate-limited 429) without
// string matching.
type AppsError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *AppsError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("civitai apps API: %s: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("civitai apps API: %s", e.Status)
}

// AppsClient GETs the CivitAI Apps catalog. It is a thin HTTP client (not the
// SDK) because the SDK has no app-listing call.
type AppsClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// appsMaxBody caps the response body read defensively (the catalog page is
// small; anything larger is treated as malformed rather than read unbounded).
const appsMaxBody = 4 << 20 // 4 MiB

// NewAppsClient builds an apps-catalog client for the given base URL and
// optional personal API token (pass "" for anonymous — browsing is anon-capable;
// a token only widens the visible cohort). The base URL defaults to
// https://civitai.com when empty.
func NewAppsClient(baseURL, token string) *AppsClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://civitai.com"
	}
	return &AppsClient{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// ListApps fetches one page of the apps catalog. It sends the configured token
// if present (harmless when anonymous), reads the body under a bounded cap, maps
// a non-2xx response to a typed *AppsError, and decodes the {items, metadata}
// envelope tolerantly (missing/unknown fields are fine). An empty items list is
// a valid response (the pre-launch reality), NOT an error.
func (c *AppsClient) ListApps(ctx context.Context, p AppsParams) (*AppsPage, error) {
	q := url.Values{}
	if p.Kind != "" {
		q.Set("kind", p.Kind)
	}
	if p.Category != "" {
		q.Set("category", p.Category)
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}

	u := c.baseURL + "/api/v1/apps"
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("apps list: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apps list: request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, appsMaxBody))
	if err != nil {
		return nil, fmt.Errorf("apps list: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &AppsError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		}
	}

	var page AppsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("apps list: decode: %w", err)
	}
	return &page, nil
}
