package comfy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
)

// --- /internal/folder_paths ---

// FolderPaths fetches GET /internal/folder_paths: ComfyUI's own map of model
// CATEGORY ("checkpoints", "loras", …) to the ABSOLUTE directories it searches
// for that category.
//
// This is the only ComfyUI endpoint that reveals a filesystem path. Measured
// against a live ComfyUI 0.27.1 (2026-08-02): /system_stats carries no path at all
// (its argv is a bare ["main.py"]), and /models returns category NAMES only. So
// this endpoint is what makes "we found your models folder" possible instead of
// "please type the path".
//
// It is best-effort by contract: an older ComfyUI answers 404, and the caller must
// degrade to asking the user rather than failing. Nothing here writes anything.
func (c *Client) FolderPaths(ctx context.Context) (map[string][]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/internal/folder_paths", nil)
	if err != nil {
		return nil, fmt.Errorf("folder_paths: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBounded(resp.Body, maxJSONBytes)
		return nil, statusError("folder_paths", resp.StatusCode, data)
	}
	data, err := readBounded(resp.Body, maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("read folder_paths: %w", err)
	}
	var fp map[string][]string
	if err := json.Unmarshal(data, &fp); err != nil {
		return nil, fmt.Errorf("parse folder_paths: %w", err)
	}
	return fp, nil
}

// ModelsRoot infers the ComfyUI models/ root from a folder_paths map: the
// directory that is the PARENT of the most categories.
//
// 🔴 Why the most-common parent and not `checkpoints[0]`:
//
//   - A category can list SEVERAL roots, and their order is not a preference.
//     Measured on the operator's live install, `checkpoints` is
//     [".../ComfyUI/models/checkpoints", ".../ComfyUI/output/checkpoints"] — taking
//     [0] happens to be right here, but extra_model_paths.yaml controls that order
//     and a user who lists a network share first would silently have downloads
//     routed onto it.
//   - The signal is overwhelming in aggregate and needs no tuning: over the same
//     install's 59 categories, .../ComfyUI/models is the parent of 57 while the
//     runner-up (.../ComfyUI/output) is the parent of 11. A 5:1 margin is not a
//     coin-flip between plausible candidates.
//
// Only categories whose directory name MATCHES the category name are counted, so a
// pack that registers an arbitrary directory (measured: comfyui-kjnodes and
// ComfyUI-Chibi-Nodes each register a path inside their own custom_nodes folder)
// cannot pull the vote toward custom_nodes/. `custom_nodes` itself is excluded for
// the same reason: its parent is the ComfyUI INSTALL root, not the models root, and
// counting it would put one vote on the wrong answer.
//
// It returns "" when nothing wins — a tie, too little agreement, or an empty/odd
// map. An empty answer means "ask the user", which is the correct degradation;
// guessing between two equally-supported roots would silently pick where gigabytes
// land.
//
// 🔴 minModelsRootVotes is a HOSTILE-PAYLOAD floor, not a tuning knob. ComfyUI is
// on the other end of an HTTP call, so this map is untrusted input, and the vote is
// on a category directory's PARENT — which need not itself look like anything.
// Without a floor a single fabricated category was enough: measured,
// {"checkpoints":["/tmp/checkpoints"]} nominated /tmp, which the re-validation
// downstream accepts because it exists and is writable. The margin on an honest
// payload is not the argument here (57 vs 11 on the operator's install) — an honest
// payload is exactly what an attacker does not send.
//
// Two agreeing categories is deliberately the SMALLEST useful floor. A real ComfyUI
// registers ~59, so this costs a genuine install nothing, and the failure direction
// is cheap: too few votes degrades to the empty form the user was going to face
// anyway. It is NOT sufficient on its own — a payload can list many fake categories
// under one parent just as easily as one — which is why the caller corroborates the
// winner against the filesystem; see web.suggestComfyModelPath.
func ModelsRoot(fp map[string][]string) string {
	votes := map[string]int{}
	for category, dirs := range fp {
		if category == "custom_nodes" {
			continue
		}
		for _, dir := range dirs {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			// Normalize to forward slashes so a Windows payload keys consistently.
			clean := path.Clean(strings.ReplaceAll(dir, "\\", "/"))
			if path.Base(clean) != category {
				continue
			}
			parent := path.Dir(clean)
			if parent == "" || parent == "." || parent == "/" {
				continue
			}
			votes[parent]++
		}
	}
	if len(votes) == 0 {
		return ""
	}
	// Sort candidates by vote count, then lexicographically, so the winner is
	// deterministic for a given payload (Go map iteration is not).
	type cand struct {
		dir string
		n   int
	}
	cands := make([]cand, 0, len(votes))
	for d, n := range votes {
		cands = append(cands, cand{d, n})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].n != cands[j].n {
			return cands[i].n > cands[j].n
		}
		return cands[i].dir < cands[j].dir
	})
	// A tie is NOT a winner: two roots with equal support are genuinely ambiguous,
	// and picking the lexicographically smaller one would be a silent coin-flip
	// about a download destination.
	if len(cands) > 1 && cands[0].n == cands[1].n {
		return ""
	}
	if cands[0].n < minModelsRootVotes {
		return ""
	}
	return cands[0].dir
}

// minModelsRootVotes is the number of agreeing categories a directory needs before
// it may be nominated at all. See ModelsRoot for why it exists and why it is 2.
const minModelsRootVotes = 2

// ModelsRootCategoryDirs lists the directories that VOTED for root — the
// `<root>/<category>` paths the payload reported and this package counted.
//
// It exists so a caller can corroborate a nomination against the filesystem without
// re-implementing (or drifting from) the matching rule above. ModelsRoot is
// deliberately PURE — it must stay unit-testable against a payload alone, and the
// package has no business stat-ing paths a remote ComfyUI named — so the existence
// check belongs to the caller that already touches the disk.
func ModelsRootCategoryDirs(fp map[string][]string, root string) []string {
	if root == "" {
		return nil
	}
	var out []string
	for category, dirs := range fp {
		if category == "custom_nodes" {
			continue
		}
		for _, dir := range dirs {
			clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/"))
			if path.Base(clean) == category && path.Dir(clean) == root {
				out = append(out, dir)
			}
		}
	}
	sort.Strings(out)
	return out
}
