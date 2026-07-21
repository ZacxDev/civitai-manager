package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	"github.com/spf13/cobra"
)

// newScanner builds a library.Scanner from the resolved app config, adding any
// extra --path directories and honouring --no-remote (a nil reader when
// offline).
func (a *app) newScanner(extraPaths []string, noRemote bool) *library.Scanner {
	paths := append([]string{}, a.cfg.LibraryPaths...)
	paths = append(paths, extraPaths...)
	opts := library.Options{
		Paths:      paths,
		ModelRoot:  a.cfg.ModelRoot,
		TrashDir:   a.cfg.TrashDir,
		Extensions: library.ExtensionSet(a.cfg.LibraryExtensions),
		NoRemote:   noRemote,
	}
	var reader civitai.Reader
	if !noRemote {
		reader = a.client
	}
	return library.NewScanner(a.store, reader, opts, a.log)
}

func newScanCmd(gf *globalFlags) *cobra.Command {
	var (
		paths    []string
		noRemote bool
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan model directories: hash, match to CivitAI, and report deletion candidates (read-only)",
		Long: "scan walks each --path (default: model_root), hashes model files\n" +
			"(reusing an mtime/size cache to skip unchanged multi-GB files), matches\n" +
			"them to CivitAI, and flags deletion candidates (superseded, duplicate,\n" +
			"broken). It NEVER moves or renames your files — use `library quarantine`\n" +
			"to act on candidates.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			sc := a.newScanner(paths, noRemote)
			report, err := sc.Scan(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(scanJSON(report))
			}
			printScanReport(report, noRemote)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&paths, "path", nil, "directory to scan (repeatable; default: model_root)")
	f.BoolVar(&noRemote, "no-remote", false, "offline: skip all CivitAI API calls (local analysis only)")
	f.BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}

func newLibraryCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Inspect and act on library deletion candidates (quarantine, restore, trash)",
	}
	cmd.AddCommand(
		newCandidatesCmd(gf),
		newQuarantineCmd(gf),
		newRestoreCmd(gf),
		newTrashCmd(gf),
	)
	return cmd
}

func newCandidatesCmd(gf *globalFlags) *cobra.Command {
	var (
		reason string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "candidates",
		Short: "List current deletion candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateReason(reason); err != nil {
				return err
			}
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			cands, err := a.store.ListCandidates(reason)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(candidatesJSON(cands))
			}
			printCandidates(cands)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&reason, "reason", "", "filter by reason: superseded, duplicate, or broken")
	f.BoolVar(&asJSON, "json", false, "emit as JSON")
	return cmd
}

func newQuarantineCmd(gf *globalFlags) *cobra.Command {
	var (
		ids    []int64
		reason string
		all    bool
		apply  bool
	)
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Move flagged candidates into the trash dir (dry-run unless --apply)",
		Long: "quarantine soft-deletes candidates by MOVING them (and their sidecars)\n" +
			"into the trash dir with an undo manifest. It never hard-deletes.\n\n" +
			"Without --apply it is a DRY-RUN that prints exactly what would move; a\n" +
			"bare `quarantine` (no selector) dry-runs over ALL current candidates.\n" +
			"--apply actually moves files and REQUIRES an explicit selector (--id,\n" +
			"--reason, or --all) so the destructive path always names its targets.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateReason(reason); err != nil {
				return err
			}
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			return quarantineRun(ctx, a, cmd.OutOrStdout(), ids, reason, all, apply)
		},
	}
	f := cmd.Flags()
	f.Int64SliceVar(&ids, "id", nil, "candidate id(s) to quarantine (repeatable)")
	f.StringVar(&reason, "reason", "", "quarantine all candidates with this reason (superseded/duplicate/broken)")
	f.BoolVar(&all, "all", false, "quarantine every current candidate")
	f.BoolVar(&apply, "apply", false, "actually move files (default: dry-run); requires a selector")
	return cmd
}

// quarantineRun resolves the selector and runs the quarantine plan (dry-run or
// apply). A bare invocation (no --id/--reason/--all) DRY-RUNS over every current
// candidate — matching the help contract. The destructive --apply path, by
// contrast, REFUSES a bare invocation: it requires an explicit selector so it
// never nukes every candidate implicitly. Factored out of the cobra RunE so it
// can be exercised with an in-memory app.
func quarantineRun(ctx context.Context, a *app, out io.Writer, ids []int64, reason string, all, apply bool) error {
	if apply && len(ids) == 0 && reason == "" && !all {
		return fmt.Errorf("--apply requires an explicit selector: --id, --reason, or --all")
	}

	targetIDs, err := resolveTargetIDs(a.store, ids, reason, all)
	if err != nil {
		return err
	}
	if len(targetIDs) == 0 {
		fmt.Fprintln(out, "No matching candidates.")
		return nil
	}

	sc := a.newScanner(nil, false)
	plan, err := sc.Quarantine(ctx, targetIDs, apply)
	if err != nil {
		return err
	}
	printQuarantinePlan(plan, out)
	return nil
}

func newRestoreCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <batchID>",
		Short: "Restore a quarantined batch to its original locations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			batchID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid batch id %q", args[0])
			}
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			sc := a.newScanner(nil, false)
			res, err := sc.Restore(ctx, batchID)
			if err != nil {
				if err == store.ErrNotFound {
					return fmt.Errorf("no quarantine batch #%d", batchID)
				}
				return err
			}
			fmt.Printf("Restored %d file(s) from batch #%d.\n", len(res.Restored), batchID)
			for _, p := range res.Restored {
				fmt.Printf("  restored: %s\n", p)
			}
			for _, p := range res.Conflicts {
				fmt.Printf("  CONFLICT (left in trash, path occupied): %s\n", p)
			}
			return nil
		},
	}
}

func newTrashCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "Manage the quarantine trash",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List quarantine batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			batches, err := a.store.ListQuarantineBatches()
			if err != nil {
				return err
			}
			if len(batches) == 0 {
				fmt.Println("Trash is empty.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "BATCH\tCREATED\tREASON\tFILES\tRESTORED")
			for _, b := range batches {
				files, _ := a.store.ListQuarantinedFiles(b.ID)
				restored := "no"
				if b.Restored() {
					restored = "yes"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n",
					b.ID, b.CreatedAt.Local().Format("2006-01-02 15:04"), b.Reason, len(files), restored)
			}
			return tw.Flush()
		},
	}
	cmd.AddCommand(list)
	return cmd
}

// --- helpers ---

func validateReason(reason string) error {
	switch reason {
	case "", store.CandidateSuperseded, store.CandidateDuplicate, store.CandidateBroken:
		return nil
	}
	return fmt.Errorf("invalid --reason %q (want superseded, duplicate, or broken)", reason)
}

// resolveTargetIDs turns the --id/--reason/--all selectors into a concrete id
// list. Explicit --id wins; otherwise it selects candidates by reason (or all).
func resolveTargetIDs(st *store.Store, ids []int64, reason string, all bool) ([]int64, error) {
	if len(ids) > 0 {
		return ids, nil
	}
	filter := reason
	if all {
		filter = ""
	}
	cands, err := st.ListCandidates(filter)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.ID)
	}
	return out, nil
}

func printScanReport(r *library.ScanReport, noRemote bool) {
	fmt.Printf("Scanned %d model file(s) across %d root(s).\n", r.FilesScanned, len(r.Roots))
	fmt.Printf("  hashed: %d   cached: %d\n", r.Hashed, r.Reused)
	if noRemote {
		fmt.Printf("  matched: %d   unmatched: %d   (offline: no API matching)\n", r.Matched, r.Unmatched)
	} else {
		fmt.Printf("  matched: %d   unmatched: %d   pending: %d\n", r.Matched, r.Unmatched, r.Pending)
	}
	fmt.Printf("  candidates: %d superseded, %d duplicate, %d broken\n", r.Superseded, r.Duplicate, r.Broken)
	fmt.Printf("  reclaimable: %s\n", humanBytes(r.Reclaimable))

	byModel := groupByModel(r.Files)
	if len(byModel) > 0 {
		fmt.Println("\nBy model:")
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, g := range byModel {
			for _, f := range g.files {
				fmt.Fprintf(tw, "  model %s\tv%s\t%s\t%s\t%s\n",
					intOrDash(f.ModelID), intOrDash(f.VersionID), statusOrCandidate(f), humanBytes(f.SizeBytes), f.Path)
			}
		}
		_ = tw.Flush()
	}
	if len(r.Candidates) > 0 {
		fmt.Println("\nDeletion candidates (act with `library quarantine`):")
		printCandidates(r.Candidates)
	}
}

func printCandidates(cands []store.LocalFile) {
	if len(cands) == 0 {
		fmt.Println("No deletion candidates.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tREASON\tSIZE\tPATH")
	for _, c := range cands {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", c.ID, c.CandidateReason, humanBytes(c.SizeBytes), c.Path)
	}
	_ = tw.Flush()
}

func printQuarantinePlan(plan *library.QuarantinePlan, out io.Writer) {
	verb := "Would move"
	if plan.Applied {
		verb = "Moved"
	}
	fmt.Fprintf(out, "%s %d file(s) (%s).\n", verb, len(plan.Moves), humanBytes(plan.TotalBytes))
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, m := range plan.Moves {
		tag := m.Reason
		if m.IsSidecar {
			tag = "sidecar"
		}
		fmt.Fprintf(tw, "  %s\t%s\t-> %s\n", tag, m.OriginalPath, m.TrashPath)
	}
	_ = tw.Flush()
	for _, sk := range plan.Skipped {
		fmt.Fprintf(out, "  SKIPPED %s: %s\n", sk.Path, sk.Reason)
	}
	if plan.Applied {
		fmt.Fprintf(out, "Batch #%d written. Undo with: civitai-manager library restore %d\n", plan.BatchID, plan.BatchID)
	} else if len(plan.Moves) > 0 {
		fmt.Fprintln(out, "Dry-run. Re-run with --apply to move these files.")
	}
}

type modelGroup struct {
	modelID int
	files   []store.LocalFile
}

func groupByModel(files []store.LocalFile) []modelGroup {
	byID := map[int]*modelGroup{}
	var order []int
	for _, f := range files {
		id := 0
		if f.ModelID != nil {
			id = *f.ModelID
		}
		g, ok := byID[id]
		if !ok {
			g = &modelGroup{modelID: id}
			byID[id] = g
			order = append(order, id)
		}
		g.files = append(g.files, f)
	}
	sort.Ints(order)
	out := make([]modelGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func statusOrCandidate(f store.LocalFile) string {
	if f.IsCandidate() {
		return f.CandidateReason
	}
	return f.Status
}

func intOrDash(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- JSON views ---

type scanJSONView struct {
	Roots       []string            `json:"roots"`
	Scanned     int                 `json:"files_scanned"`
	Hashed      int                 `json:"hashed"`
	Cached      int                 `json:"cached"`
	Matched     int                 `json:"matched"`
	Unmatched   int                 `json:"unmatched"`
	Pending     int                 `json:"pending"`
	Superseded  int                 `json:"superseded"`
	Duplicate   int                 `json:"duplicate"`
	Broken      int                 `json:"broken"`
	Reclaimable int64               `json:"reclaimable_bytes"`
	Candidates  []candidateJSONView `json:"candidates"`
}

type candidateJSONView struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	ModelID   *int   `json:"model_id,omitempty"`
	VersionID *int   `json:"version_id,omitempty"`
}

func scanJSON(r *library.ScanReport) scanJSONView {
	return scanJSONView{
		Roots: r.Roots, Scanned: r.FilesScanned, Hashed: r.Hashed, Cached: r.Reused,
		Matched: r.Matched, Unmatched: r.Unmatched, Pending: r.Pending,
		Superseded: r.Superseded, Duplicate: r.Duplicate, Broken: r.Broken,
		Reclaimable: r.Reclaimable, Candidates: candidatesJSON(r.Candidates),
	}
}

func candidatesJSON(cands []store.LocalFile) []candidateJSONView {
	out := make([]candidateJSONView, 0, len(cands))
	for _, c := range cands {
		out = append(out, candidateJSONView{
			ID: c.ID, Path: c.Path, Reason: c.CandidateReason, SHA256: c.SHA256,
			SizeBytes: c.SizeBytes, ModelID: c.ModelID, VersionID: c.VersionID,
		})
	}
	return out
}

// humanBytes renders a byte count compactly (kept local to the CLI package).
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
