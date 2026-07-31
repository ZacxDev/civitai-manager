package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// ===========================================================================
// The "this workflow USES a file from this model" relation.
// ===========================================================================
//
// 🔴 This is a DIFFERENT relation from workflows.model_id / version_id, and the
// two must never be conflated in the UI:
//
//   - workflows.model_id  — "this workflow was IMPORTED FROM this model". A hard,
//     recorded fact stamped by the importer (discover_workflow_import.go).
//   - workflows.resources — "this workflow REFERENCES a file that belongs to this
//     model". An INFERRED relation, derived here.
//
// A workflow can have one, both or neither. Telling a user a workflow "came from"
// a model it merely loads a LoRA from would be a straight factual error.
//
// ---------------------------------------------------------------------------
// THE MATCHING RULE, and what it does NOT catch
// ---------------------------------------------------------------------------
// A ComfyUI graph names its resources by FILENAME ONLY — there is no hash, no id
// and no URL in a widget value — so filename is the only join key that exists.
// The rule is therefore deliberately conservative, because a false "this workflow
// uses this model" is worse than a miss:
//
//  1. The candidate filenames come from the LOCAL INDEX, not from the remote
//     model: `local_files` rows whose `model_id` is this model. Those rows were
//     linked by SHA-256 during a scan, so "this basename belongs to this model" is
//     backed by a content hash rather than by a name the API happened to report.
//  2. A basename that ANY OTHER model also claims locally is DROPPED as ambiguous
//     — the same "do not guess" stance FindVersionByFileName and
//     LocalFileByBasename already take.
//  3. Comparison is on the BASENAME (a resource may carry a folder prefix, e.g.
//     "seg-a/waiNSFW_v140.safetensors") and is CASE-INSENSITIVE, matching every
//     other basename comparison in this package. Case-insensitivity is not
//     laxness: the app's own targets include case-insensitive filesystems (APFS,
//     NTFS) where two spellings ARE the same file, and CivitAI/ComfyUI casing
//     drifts between the download and the graph.
//
// WHAT IT MISSES (all deliberate, all silent):
//   - a model the user has NOT downloaded (or whose files never got matched to a
//     civitai model) contributes NO candidate basenames, so it lists nothing —
//     the relation is only as good as the local scan;
//   - a file RENAMED on disk no longer shares a basename with the graph's
//     reference;
//   - a basename shared with another model is dropped entirely (rule 2), so a
//     genuine use of a generically-named file is a miss.
//
// WHAT IT CAN STILL GET WRONG: a graph referencing a DIFFERENT file that happens
// to share a basename with the user's copy, where no other local file shares that
// basename to trip rule 2. Nothing in a ComfyUI graph can distinguish those two
// files, so this residue is irreducible — it is stated in the UI's own wording
// ("references a file named …") rather than papered over.

// ResourceBasename normalizes ONE workflow resource entry to the basename the
// local index is keyed by: backslashes folded to forward slashes (a
// Windows-authored graph writes "seg-a\foo.safetensors") and the directory
// prefix stripped.
//
// It uses path.Base rather than filepath.Base so the result does not depend on
// the OS running the query — a database written on Windows must resolve
// identically when read on Linux.
//
// This is the SINGLE definition of "what part of a resource entry is the
// filename": the web layer's resource chips call it too, so the presence mark on
// a chip and the usage list on a model page can never disagree about what a
// resource is called.
func ResourceBasename(res string) string {
	s := strings.TrimSpace(strings.ReplaceAll(res, `\`, "/"))
	if s == "" {
		return ""
	}
	return path.Base(s)
}

// WorkflowUsage is one workflow that REFERENCES a file belonging to some model.
//
// It deliberately does NOT carry the graph: the column is by far the largest in
// the table (a full ComfyUI graph, routinely hundreds of KB) and nothing about
// rendering a usage list needs it. Selecting it would make a model page pay
// megabytes to draw a handful of links.
type WorkflowUsage struct {
	ID   int64
	Name string
	// Source is the workflow's provenance string (imported/scanned/png/authored).
	Source string
	// ModelID/VersionID are the IMPORTED-FROM linkage, carried so a caller can tell
	// the two relations apart on the same row — it is common and correct for a
	// workflow imported from model A to USE a file from model B.
	ModelID   *int
	VersionID *int
	// Matched are the resource entries (as written in the graph, folder prefix and
	// all) that resolved to one of the queried basenames. It is what lets the UI
	// name the actual file instead of asserting a bare relationship.
	Matched []string
}

// workflowUsageScanCap bounds how many workflow rows ONE usage query examines.
//
// The narrowing predicate is a substring LIKE over a JSON text column, which no
// index can serve, so this is what keeps a model page's cost fixed instead of
// growing with the library. It is generous relative to any realistic personal
// workflow library while still being a hard ceiling: past it the list is
// truncated (newest first) rather than the query getting slower.
const workflowUsageScanCap = 200

// basenameChunk is how many basenames ONE narrowing statement may carry.
//
// 🔴 IT EXISTS BECAUSE SQLite HAS AN EXPRESSION-DEPTH LIMIT, NOT A PARAMETER
// LIMIT. Both queries below narrow with a chain of OR'd LIKEs, and SQLite parses
// `a OR b OR c …` into a LEFT-DEEP tree whose depth grows with the number of
// operands. Past `SQLITE_MAX_EXPR_DEPTH` (1000 in the modernc build) the whole
// statement fails with:
//
//	SQL logic error: Expression tree is too large (maximum depth 1000)
//
// Measured on this build, unchunked (the exact thresholds this constant retires):
//
//	UnambiguousModelFileBasenames  last OK 490,  first failure 500
//	ListWorkflowsUsingFiles        last OK 980,  first failure 1000
//
// THE TWO NUMBERS DIFFER BY 2× BECAUSE THE STATEMENTS DIFFER, not because of
// anything about the data: the ambiguity probe emits TWO OR-operands per
// basename (a `(path LIKE ? OR path = ?)` group for POSIX paths plus a
// `path LIKE ?` for Windows-style ones), while the workflow query emits ONE
// (`resources LIKE ?`). So the ambiguity probe hits the ceiling at half as many
// basenames, and any chunk size must be sized for THAT one.
//
// Why 100 and not something near the measured cliff: the limit is on DEPTH, so
// it moves the moment anyone adds another clause per basename — sizing at the
// threshold would make a one-line future edit silently re-introduce the bug. At
// 100 the worst statement is 200 deep (5× headroom), and even a hypothetical
// four-clauses-per-basename rewrite would only reach 400.
//
// The failure mode this prevents is nasty precisely because it is quiet: the
// caller (Server.workflowsUsingModel) is fail-soft, so a model with ≥500 locally
// indexed basenames would just lose its "Workflows that use this model" section
// on every render, forever, with nothing but a log line to say why.
const basenameChunk = 100

// chunkStrings splits s into consecutive runs of at most n, preserving order. A
// non-positive n (or an empty input) yields the input as a single chunk / nil, so
// a caller can never accidentally produce an unbounded loop.
func chunkStrings(s []string, n int) [][]string {
	if len(s) == 0 {
		return nil
	}
	if n <= 0 {
		return [][]string{s}
	}
	out := make([][]string, 0, (len(s)+n-1)/n)
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// UnambiguousModelFileBasenames returns the basenames of this model's LOCALLY
// INDEXED files, minus any basename that another model also claims locally.
//
// Two indexed reads, both bounded:
//
//	A) local_files WHERE model_id = ?          — served by idx_local_files_model.
//	B) local_files WHERE path LIKE '%/<name>'  — one statement for ALL candidate
//	   basenames OR'd together, mirroring HasLocalFileNamed's narrow-then-verify
//	   shape (the SQL LIKE only narrows; the exact case-insensitive compare in Go
//	   is the gate).
//
// A blank/zero model id yields nil without touching the database.
func (s *Store) UnambiguousModelFileBasenames(ctx context.Context, modelID int) ([]string, error) {
	if modelID <= 0 {
		return nil, nil
	}

	// (A) every locally indexed file linked to this model.
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM local_files WHERE model_id = ? ORDER BY path`, modelID)
	if err != nil {
		return nil, err
	}
	// order preserves determinism; lower(basename) -> the first-seen spelling.
	var order []string
	byLower := map[string]string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		base := ResourceBasename(p)
		if base == "" {
			continue
		}
		low := strings.ToLower(base)
		if _, seen := byLower[low]; seen {
			continue
		}
		byLower[low] = base
		order = append(order, low)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(order) == 0 {
		return nil, nil
	}

	// (B) does any OTHER model claim one of these basenames locally? If so that
	// basename cannot identify this model and is dropped — never guessed at.
	//
	// CHUNKED — see basenameChunk. Each basename contributes TWO OR-operands here,
	// so this statement hits SQLITE_MAX_EXPR_DEPTH at twice the rate of the one in
	// ListWorkflowsUsingFiles.
	ambiguous := map[string]bool{}
	for _, chunk := range chunkStrings(order, basenameChunk) {
		var (
			clauses []string
			args    []any
		)
		args = append(args, modelID)
		for _, low := range chunk {
			name := byLower[low]
			clauses = append(clauses, "(path LIKE ? OR path = ?)")
			args = append(args, "%/"+name, name)
			// Windows-style paths are stored verbatim, so the suffix must be probed
			// with both separators.
			clauses = append(clauses, "path LIKE ?")
			args = append(args, `%\`+name)
		}
		q := `SELECT path FROM local_files
			WHERE model_id IS NOT NULL AND model_id <> ? AND (` + strings.Join(clauses, " OR ") + `)`
		orows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for orows.Next() {
			var p string
			if err := orows.Scan(&p); err != nil {
				orows.Close()
				return nil, err
			}
			// The LIKE only narrowed; this exact compare is the gate, so
			// "myvae.safetensors" cannot poison "vae.safetensors". It is checked
			// against the FULL basename set (byLower), not just this chunk, so a
			// cross-chunk hit is still recorded.
			low := strings.ToLower(ResourceBasename(p))
			if _, ours := byLower[low]; ours {
				ambiguous[low] = true
			}
		}
		if err := orows.Err(); err != nil {
			orows.Close()
			return nil, err
		}
		orows.Close()
	}

	out := make([]string, 0, len(order))
	for _, low := range order {
		if !ambiguous[low] {
			out = append(out, byLower[low])
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// ListWorkflowsUsingFiles returns the workflows whose `resources` reference any
// of the given basenames, newest first.
//
// No N+1 and no per-render full scan of the graph column:
//
//	SELECT id, name, source, model_id, version_id, resources
//	FROM workflows
//	WHERE resources IS NOT NULL AND (resources LIKE ? OR …)
//	ORDER BY id DESC LIMIT workflowUsageScanCap
//
// The LIKE list only NARROWS. It is a substring probe over the JSON array text,
// so it over-matches in two harmless ways — a basename embedded in a longer
// filename, and LIKE's own `_`/`%` wildcards, which real filenames contain — and
// both are corrected by the exact case-insensitive basename compare in Go below.
// The values are bound parameters, so no filename can alter the statement.
//
// It is normally ONE statement, and becomes one per basenameChunk past 100
// basenames — see basenameChunk for why the OR chain cannot grow unbounded. Three
// things make chunking SAFE rather than merely non-erroring, and all three matter:
//
//   - the exact-compare gate uses the FULL `want` set, never the chunk's, so a
//     row's Matched list is identical regardless of which chunk found it;
//   - rows are deduped by workflow id across chunks (a workflow referencing files
//     from two different chunks would otherwise be listed twice);
//   - the per-statement LIMIT cannot decide the final ordering, so the merged set
//     is re-sorted by id DESC and capped once at the end. Capping per chunk would
//     let an early chunk's OLDER rows crowd out a later chunk's newer ones.
//
// An empty basename list returns nil WITHOUT querying: an unbounded "match
// everything" would be exactly the full-table scan this is shaped to avoid.
func (s *Store) ListWorkflowsUsingFiles(ctx context.Context, basenames []string) ([]WorkflowUsage, error) {
	// The full deduped set, used BOTH to build the chunks and — critically — as the
	// exact-compare gate for every row in every chunk.
	want := map[string]bool{}
	var uniq []string
	for _, b := range basenames {
		b = strings.TrimSpace(b)
		low := strings.ToLower(b)
		if low == "" || want[low] {
			continue
		}
		want[low] = true
		uniq = append(uniq, b)
	}
	if len(uniq) == 0 {
		return nil, nil
	}

	var out []WorkflowUsage
	seen := map[int64]bool{}
	for _, chunk := range chunkStrings(uniq, basenameChunk) {
		clauses := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		for _, b := range chunk {
			clauses = append(clauses, "resources LIKE ?")
			args = append(args, "%"+b+"%")
		}
		args = append(args, workflowUsageScanCap)

		q := `SELECT id, name, source, model_id, version_id, resources
			FROM workflows
			WHERE resources IS NOT NULL AND (` + strings.Join(clauses, " OR ") + `)
			ORDER BY id DESC LIMIT ?`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				u         WorkflowUsage
				modelID   sql.NullInt64
				versionID sql.NullInt64
				resources sql.NullString
			)
			if err := rows.Scan(&u.ID, &u.Name, &u.Source, &modelID, &versionID, &resources); err != nil {
				rows.Close()
				return nil, err
			}
			if seen[u.ID] || !resources.Valid || resources.String == "" {
				continue
			}
			var list []string
			if json.Unmarshal([]byte(resources.String), &list) != nil {
				// A malformed resources column must not fail the whole read — the same
				// stance scanWorkflow takes. It simply contributes no usage.
				continue
			}
			for _, res := range list {
				if want[strings.ToLower(ResourceBasename(res))] {
					u.Matched = append(u.Matched, res)
				}
			}
			if len(u.Matched) == 0 {
				continue // a LIKE false positive, rejected by the exact compare
			}
			if modelID.Valid {
				v := int(modelID.Int64)
				u.ModelID = &v
			}
			if versionID.Valid {
				v := int(versionID.Int64)
				u.VersionID = &v
			}
			seen[u.ID] = true
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// Newest first ACROSS chunks, then one final cap. Both are no-ops in the
	// single-chunk case, which is every realistic library.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > workflowUsageScanCap {
		out = out[:workflowUsageScanCap]
	}
	return out, nil
}

// ListWorkflowsUsingModel is the model-page entry point: the workflows in the
// library that reference a file belonging to this model, newest first.
//
// It is the composition of the two reads above — candidate basenames from the
// local index, then one narrowed workflow query — so a model page costs THREE
// bounded statements in total, regardless of library size, and never one per
// workflow.
//
// The result EXCLUDES nothing on the basis of the imported-from linkage: a
// workflow that was imported from this model AND uses its files legitimately
// appears in both relations, and it is the caller's job to label them
// separately.
func (s *Store) ListWorkflowsUsingModel(ctx context.Context, modelID int) ([]WorkflowUsage, error) {
	names, err := s.UnambiguousModelFileBasenames(ctx, modelID)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	return s.ListWorkflowsUsingFiles(ctx, names)
}
