// Package civitai is a thin wrapper over the official pkg/civitai SDK. It
// narrows the SDK's Reader/Downloader surface to the methods this app actually
// uses, which gives the poller and download worker small interfaces to program
// against and lets tests supply in-memory fakes with no network.
package civitai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/civitai/cli/pkg/civitai"
)

// Re-export the SDK error sentinels so callers classify failures without
// importing the SDK directly.
var (
	ErrNotFound     = sdk.ErrNotFound
	ErrUnauthorized = sdk.ErrUnauthorized
	ErrRateLimited  = sdk.ErrRateLimited
	ErrNetwork      = sdk.ErrNetwork
	ErrBadRequest   = sdk.ErrBadRequest
)

// Type aliases for the SDK model types the app passes around.
type (
	ModelDetail         = sdk.ModelDetail
	ModelVersionDetail  = sdk.ModelVersionDetail
	ModelVersionSummary = sdk.ModelVersionSummary
	ModelVersionFile    = sdk.ModelVersionFile
	ModelListItem       = sdk.ModelListItem
	ModelSearchResult   = sdk.ModelSearchResult
	CreatorSearchResult = sdk.CreatorSearchResult
	ImageSearchResult   = sdk.ImageSearchResult
	Metadata            = sdk.Metadata
	Creator             = sdk.Creator
	FileHashes          = sdk.FileHashes
)

// PrimaryFile re-exports the SDK's primary-file selector.
var PrimaryFile = sdk.PrimaryFile

// Reader is the read surface the poller and web UI depend on. *sdk.Client
// satisfies it; tests supply a fake.
type Reader interface {
	GetModel(ctx context.Context, id string) (*ModelDetail, []byte, error)
	GetModelVersion(ctx context.Context, id string) (*ModelVersionDetail, []byte, error)
	// GetModelVersionByHash resolves a model version from any file hash
	// (SHA256, AutoV2, …). The library scanner uses it to match local files to
	// their CivitAI version without knowing the model/version id up front.
	GetModelVersionByHash(ctx context.Context, hash string) (*ModelVersionDetail, []byte, error)
	SearchModels(ctx context.Context, q url.Values) (*ModelSearchResult, error)
	SearchCreators(ctx context.Context, q url.Values) (*CreatorSearchResult, error)
	SearchImages(ctx context.Context, q url.Values) (*ImageSearchResult, error)
}

// Downloader is the download surface the queue worker depends on.
type Downloader interface {
	DownloadFile(ctx context.Context, fileURL string) (*http.Response, error)
}

// Client is the composite interface a live *sdk.Client provides.
type Client interface {
	Reader
	Downloader
}

// compile-time assertion that the SDK client satisfies our narrowed interface.
var _ Client = (*sdk.Client)(nil)

// New builds a live SDK client for the given base URL and personal API token
// (pass "" for anonymous read-only access).
func New(baseURL, token string) *sdk.Client {
	return sdk.New(baseURL, token)
}

// SelectFile chooses which file of a version to download. When fileTypePref is
// non-empty it prefers the primary-or-first file whose Type matches (case
// -insensitive); otherwise, or when no file matches, it falls back to the SDK's
// PrimaryFile. Returns nil for a version with no files.
func SelectFile(files []ModelVersionFile, fileTypePref string) *ModelVersionFile {
	if len(files) == 0 {
		return nil
	}
	if pref := strings.TrimSpace(fileTypePref); pref != "" {
		for i := range files {
			if strings.EqualFold(files[i].Type, pref) {
				return &files[i]
			}
		}
	}
	return PrimaryFile(files)
}

var modelURLRe = regexp.MustCompile(`/models/(\d+)`)

// ParseModelRef extracts a numeric model id from either a bare id ("12345") or
// a civitai.com model URL (e.g. https://civitai.com/models/12345/some-slug or
// .../models/12345?modelVersionId=678). It returns an error for anything else.
func ParseModelRef(ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, errors.New("empty model reference")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		if id <= 0 {
			return 0, fmt.Errorf("invalid model id %q", ref)
		}
		return id, nil
	}
	if m := modelURLRe.FindStringSubmatch(ref); m != nil {
		id, err := strconv.Atoi(m[1])
		if err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("could not parse a model id from %q (expected a number or a civitai.com/models/<id> URL)", ref)
}

// FirstImageURL best-effort extracts the first image URL from a raw
// model-version JSON body (the []byte returned by GetModelVersion). The typed
// ModelVersionDetail does not carry images, but the raw payload does under
// `images[].url`. Returns "" when none is present.
func FirstImageURL(rawVersionJSON []byte) string {
	var body struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(rawVersionJSON, &body); err != nil {
		return ""
	}
	for _, im := range body.Images {
		if strings.TrimSpace(im.URL) != "" {
			return im.URL
		}
	}
	return ""
}
