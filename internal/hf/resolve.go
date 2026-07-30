package hf

import (
	"context"
	"errors"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ErrNotFound signals a missing repo/file (an HF 404). Callers treat it as a miss.
var ErrNotFound = errors.New("hf: not found")

// SourceCurated / SourceSearch identify how a Match was resolved. A curated match
// comes from the hand-verified filename→repo table (highest precision); a search
// match comes from the best-effort search→tree exact-basename confirmation.
const (
	SourceCurated = "curated"
	SourceSearch  = "search"
)

// Match is a resolved HuggingFace file candidate for a missing model FILENAME.
type Match struct {
	// Repo is the HuggingFace repo id, e.g. "Bingsu/adetailer".
	Repo string
	// Path is the file path WITHIN the repo (may include a subdir), matched on
	// basename against the workflow reference.
	Path string
	// FileName is Path's basename (what gets written locally).
	FileName string
	// Revision is the concrete commit sha the URL is pinned to (reproducible).
	Revision string
	// URL is the https://huggingface.co/{repo}/resolve/{revision}/{path} download URL.
	URL string
	// SHA256 is the file's git-LFS oid (its content sha256), or "" for a non-LFS
	// (small) file that exposes only a git-blob sha1 we cannot pin against.
	SHA256 string
	// Gated is true when the repo is gated (gated != false) or private — such repos
	// cannot be auto-downloaded and degrade to a link.
	Gated bool
	// Source is SourceCurated or SourceSearch.
	Source string
	// RecognizedOrg is true when the repo owner is in the recognized-org allowlist.
	RecognizedOrg bool
	// Subdir is the ComfyUI models/ subdirectory this file installs into (e.g. "vae",
	// "ultralytics/bbox"), or "" when it cannot be determined (then it is link-only).
	Subdir string
	// License is the repo's declared license tag when known (surfaced in the UI).
	License string
	// Downloads / Likes are popularity signals (tiebreak + display).
	Downloads int
	Likes     int
}

// AutoDownloadEligible reports whether this match may be AUTO-downloaded (vs shown
// as a link only). Auto is permitted ONLY for a curated-map hit OR a recognized-org
// repo, AND a non-gated repo, AND a determinable ComfyUI subdir, AND a captured
// sha256 to verify the bytes against. Everything else is link-only.
func (m *Match) AutoDownloadEligible() bool {
	if m == nil || m.Gated {
		return false
	}
	if m.Subdir == "" || m.SHA256 == "" {
		return false
	}
	return m.Source == SourceCurated || m.RecognizedOrg
}

// PageURL is the human HuggingFace page for the repo (scheme-validated external
// link target for the "Open on HuggingFace" degrade path).
func (m *Match) PageURL() string {
	return DefaultBaseURL + "/" + m.Repo
}

// recognizedOrgs are repo owners trusted enough that an EXACT-basename, non-gated,
// sha-verified match from them may be auto-downloaded even via the search fallback.
// Lowercased for case-insensitive comparison.
var recognizedOrgs = map[string]bool{
	"bingsu":            true,
	"madebyollin":       true,
	"h94":               true,
	"comfyanonymous":    true,
	"comfy-org":         true,
	"stabilityai":       true,
	"black-forest-labs": true,
	"city96":            true,
	"lllyasviel":        true,
	"ai-forever":        true,
}

func orgRecognized(repo string) bool {
	owner, _, _ := strings.Cut(repo, "/")
	return recognizedOrgs[strings.ToLower(strings.TrimSpace(owner))]
}

// curatedEntry maps a filename family (by regexp on the basename) to a canonical
// repo + a function computing the ComfyUI subdir (which for detectors depends on the
// reference's bbox//segm/ prefix). Entries are the hand-verified trusted set.
type curatedEntry struct {
	pattern *regexp.Regexp
	repo    string
	subdir  func(refName string) string
}

// curatedMap is the small, clearly-sourced filename→repo table (see
// claudedocs/HUGGINGFACE-FALLBACK-RESEARCH.md §5). Order matters: the first pattern
// that matches the basename wins.
var curatedMap = []curatedEntry{
	{
		// adetailer / ultralytics YOLO detectors: face_yolov8*.pt, face_yolov9c.pt,
		// hand_yolov8*.pt, person_yolov8*-seg.pt, deepfashion2_yolov8s-seg.pt, …
		pattern: regexp.MustCompile(`(?i)_yolov\d`),
		repo:    "Bingsu/adetailer",
		subdir:  detectorSubdir,
	},
	{
		// SDXL VAE fp16 fix: sdxl_vae.safetensors / sdxl-vae-fp16-fix.safetensors.
		pattern: regexp.MustCompile(`(?i)sdxl[._-]?vae`),
		repo:    "madebyollin/sdxl-vae-fp16-fix",
		subdir:  fixedSubdir("vae"),
	},
	{
		// SD1.5 VAE ft-mse: vae-ft-mse-840000-ema-pruned.safetensors.
		pattern: regexp.MustCompile(`(?i)vae-ft-mse`),
		repo:    "stabilityai/sd-vae-ft-mse",
		subdir:  fixedSubdir("vae"),
	},
	{
		// IP-Adapter weights: ip-adapter*.safetensors / .bin.
		pattern: regexp.MustCompile(`(?i)^ip[-_]?adapter`),
		repo:    "h94/IP-Adapter",
		subdir:  fixedSubdir("ipadapter"),
	},
	{
		// ControlNet v1.1 fp16: control_v11*.safetensors.
		pattern: regexp.MustCompile(`(?i)^control_v11`),
		repo:    "comfyanonymous/ControlNet-v1-1_fp16_safetensors",
		subdir:  fixedSubdir("controlnet"),
	},
	{
		// Flux text encoders: clip_l.safetensors, t5xxl_*.safetensors.
		pattern: regexp.MustCompile(`(?i)^(clip_l|t5xxl)`),
		repo:    "comfyanonymous/flux_text_encoders",
		subdir:  fixedSubdir("text_encoders"),
	},
	{
		// Real-ESRGAN upscalers: RealESRGAN_x4plus.pth, RealESRGAN_x4plus_anime_6B.pth.
		pattern: regexp.MustCompile(`(?i)^realesrgan`),
		repo:    "ai-forever/Real-ESRGAN",
		subdir:  fixedSubdir("upscale_models"),
	},
}

func fixedSubdir(s string) func(string) string {
	return func(string) string { return s }
}

// detectorSubdir routes an ultralytics detector to ComfyUI's models/ultralytics/{bbox,segm}
// based on the reference's leading path segment (e.g. "bbox/face_yolov9c.pt"), which
// is how Impact-Pack's UltralyticsDetectorProvider organizes its model_name values.
// A ref with no such prefix defaults to bbox (the common case).
func detectorSubdir(refName string) string {
	norm := strings.ToLower(strings.ReplaceAll(refName, "\\", "/"))
	switch {
	case strings.HasPrefix(norm, "segm/"):
		return "ultralytics/segm"
	case strings.HasPrefix(norm, "bbox/"):
		return "ultralytics/bbox"
	default:
		return "ultralytics/bbox"
	}
}

// baseName returns the normalized basename of a workflow model reference.
func baseName(refName string) string {
	return path.Base(strings.ReplaceAll(strings.TrimSpace(refName), "\\", "/"))
}

// CuratedFamilyMatch reports whether refName looks auto-installable to the resolver
// WITHOUT asking the network: either its basename belongs to one of the curated
// filename families above, or it carries the detector `bbox/`/`segm/` prefix that
// searchSubdir routes on. It is PURE and LOCAL — no network, no client — so a caller
// can consult it while RENDERING.
//
// The prefix half matters: a non-curated detector ref (say `bbox/my_custom_det.pt`)
// gets a real Subdir from searchSubdir and can therefore auto-install off a
// recognized-org search hit. Testing only curatedLookup called those "may not be
// found" when in fact they usually are — a false negative in exactly the direction
// that makes a warning untrustworthy.
//
// LIMITATION (unavoidable here, and the reason this is a NECESSARY condition and not a
// promise): true means "the resolver has a destination rule for this name", not "this
// will install". Whether the repo still contains the file, whether it is gated, and
// whether it passes AutoDownloadEligible are all NETWORK facts that only Resolve can
// establish, so a true here can still end in a decline. Callers must treat it as
// "worth attempting" and must not use it to promise success.
func CuratedFamilyMatch(refName string) bool {
	b := baseName(refName)
	if b == "" || b == "." || b == ".." {
		return false
	}
	if curatedLookup(b) != nil {
		return true
	}
	// Mirrors searchSubdir's detector-prefix rule.
	norm := strings.ToLower(strings.ReplaceAll(refName, "\\", "/"))
	return strings.HasPrefix(norm, "bbox/") || strings.HasPrefix(norm, "segm/")
}

// curatedLookup returns the curated entry whose pattern matches basename, or nil.
func curatedLookup(basename string) *curatedEntry {
	for i := range curatedMap {
		if curatedMap[i].pattern.MatchString(basename) {
			return &curatedMap[i]
		}
	}
	return nil
}

// Resolve maps a workflow model FILENAME reference to a downloadable HuggingFace
// file. It tries the curated map first (highest precision), then a best-effort
// search→tree exact-basename confirmation. It returns (match, true, nil) on a
// confirmed exact-basename match, (nil, false, nil) on a clean miss, or an error on
// a transport/decoding failure. Even a gated match is returned (ok=true) so the UI
// can render the link-only degrade with the "requires accepting terms" hint.
func (c *Client) Resolve(ctx context.Context, refName string) (*Match, bool, error) {
	basename := baseName(refName)
	if basename == "" || basename == "." || basename == ".." {
		return nil, false, nil
	}

	// 1. Curated map (trusted set).
	if ce := curatedLookup(basename); ce != nil {
		m, err := c.matchInRepo(ctx, ce.repo, basename)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, false, err
		}
		if m != nil {
			m.Source = SourceCurated
			m.RecognizedOrg = orgRecognized(m.Repo)
			m.Subdir = ce.subdir(refName)
			return m, true, nil
		}
		// Curated repo did not contain the file — fall through to search.
	}

	// 2. Search fallback (best-effort).
	m, err := c.searchResolve(ctx, refName, basename)
	if err != nil {
		return nil, false, err
	}
	if m == nil {
		return nil, false, nil
	}
	return m, true, nil
}

// matchInRepo fetches a repo's info + file tree and returns a Match for the file
// whose basename equals want (exact, case-sensitive). Returns nil when the repo has
// no such file. Captures gated/private, the pinned commit sha, and the LFS sha256.
func (c *Client) matchInRepo(ctx context.Context, repo, want string) (*Match, error) {
	info, err := c.getModelInfo(ctx, repo)
	if err != nil {
		return nil, err
	}
	revision := strings.TrimSpace(info.SHA)
	if revision == "" {
		revision = "main"
	}
	tree, err := c.getTree(ctx, repo, revision)
	if err != nil {
		return nil, err
	}
	for _, f := range tree {
		if f.Type != "file" {
			continue
		}
		if path.Base(f.Path) != want {
			continue
		}
		m := &Match{
			Repo:      repo,
			Path:      f.Path,
			FileName:  path.Base(f.Path),
			Revision:  revision,
			URL:       resolveURL(c.base, repo, revision, f.Path),
			SHA256:    strings.ToLower(strings.TrimSpace(f.LFS.OID)),
			Gated:     info.gatedOrPrivate(),
			License:   info.license(),
			Downloads: info.Downloads,
			Likes:     info.Likes,
		}
		return m, nil
	}
	return nil, nil
}

// searchResolve runs the two-step search: repo search by a cleaned query, then an
// exact-basename confirmation inside the top candidates. It ranks confirmed matches
// by downloads and returns the best. A recognized-org confirmed match is preferred
// as a tiebreak. Returns nil on no confirmed exact match.
func (c *Client) searchResolve(ctx context.Context, refName, want string) (*Match, error) {
	query := cleanQuery(want)
	if query == "" {
		return nil, nil
	}
	repos, err := c.searchModels(ctx, query, searchCandidateLimit)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var best *Match
	var checked int
	for _, r := range repos {
		if checked >= searchTreeProbes {
			break
		}
		checked++
		m, err := c.matchInRepo(ctx, r.ID, want)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if m == nil {
			continue
		}
		m.Source = SourceSearch
		m.RecognizedOrg = orgRecognized(m.Repo)
		m.Subdir = c.searchSubdir(refName, m)
		if best == nil || betterMatch(m, best) {
			best = m
		}
	}
	return best, nil
}

// searchSubdir infers a ComfyUI subdir for a search-fallback match. It reuses the
// curated family's subdir rule when the basename matches a curated pattern (so a
// recognized-org mirror of a known family still routes), and the detector-prefix
// rule for a .pt with a bbox//segm/ prefix. "" when undeterminable (link-only).
func (c *Client) searchSubdir(refName string, m *Match) string {
	if ce := curatedLookup(m.FileName); ce != nil {
		return ce.subdir(refName)
	}
	norm := strings.ToLower(strings.ReplaceAll(refName, "\\", "/"))
	if strings.HasPrefix(norm, "bbox/") || strings.HasPrefix(norm, "segm/") {
		return detectorSubdir(refName)
	}
	return ""
}

// betterMatch reports whether a should rank above b: a recognized-org match beats an
// unrecognized one; otherwise higher downloads win.
func betterMatch(a, b *Match) bool {
	if a.RecognizedOrg != b.RecognizedOrg {
		return a.RecognizedOrg
	}
	return a.Downloads > b.Downloads
}

// resolveURL builds the pinned /resolve download URL. Every path segment —
// including the repo's owner/name — is url.PathEscape'd; the host stays locked to
// the allowlist by the client dialer regardless, this just keeps a repo value from
// the HF API from producing a malformed path.
func resolveURL(base, repo, revision, filePath string) string {
	escSegs := func(p string) string {
		segs := strings.Split(p, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		return strings.Join(segs, "/")
	}
	return strings.TrimRight(base, "/") + "/" + escSegs(repo) + "/resolve/" + url.PathEscape(revision) + "/" + escSegs(filePath)
}

// searchCandidateLimit bounds how many repos the search returns; searchTreeProbes
// bounds how many of them we fetch a file tree for (each is an API call).
const (
	searchCandidateLimit = 10
	searchTreeProbes     = 5
)

// versionTokenRe strips a trailing version token from a cleaned query token stream.
var versionTokenRe = regexp.MustCompile(`(?i)^v?\d[\d.]*$`)

// cleanQuery turns a filename basename into a repo-search query: drop the extension,
// split on separators/camelCase-ish boundaries, and drop pure version tokens. HF
// search is repo-NAME level (it does not index filenames), so this is a best-effort
// query used only for the fallback path.
func cleanQuery(basename string) string {
	s := basename
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i]
	}
	s = strings.NewReplacer("_", " ", "-", " ", ".", " ", "/", " ").Replace(s)
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if versionTokenRe.MatchString(f) {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// sortReposByDownloads sorts repos descending by downloads (search already sorts,
// but this is a defensive local re-sort for deterministic candidate order).
func sortReposByDownloads(repos []repoSummary) {
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Downloads > repos[j].Downloads })
}
