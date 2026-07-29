package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The gated node-pack install and the explicit ComfyUI restart.
//
// This is DELEGATION, not execution: civitai-manager never writes to
// custom_nodes/, never runs pip, and never picks a pack for the user.
// ComfyUI-Manager does the work under its own security policy, and every step is
// reported honestly — in particular, success is claimed ONLY when Manager's own
// installed-set diff shows the pack landed. The obvious signals do not work:
// Manager V3 destroys its per-task result immediately after a websocket broadcast
// and never exposes it over REST, and its queue reports is_processing==false
// BEFORE you start, so a naive poll exits at once and would report success for
// work that never ran.

// Install job timings. The budget is a RUNAWAY BACKSTOP, not the normal
// termination path: a pack with heavy Python requirements can legitimately take
// minutes.
const (
	// nodepackInstallBudget bounds the whole install so a wedged Manager cannot
	// leak a goroutine forever.
	nodepackInstallBudget = 10 * time.Minute
	// nodepackPollInterval is the cadence of the queue-status/installed-diff poll.
	nodepackPollInterval = 1 * time.Second
	// nodepackMinSettle is how long we keep polling before believing an
	// entirely-idle queue. It exists because Manager reports is_processing==false
	// before a queue is started: without this grace period a fast exit would read
	// "never started" as "already finished".
	nodepackMinSettle = 10 * time.Second
	// nodepackProbeTimeout bounds the short synchronous Manager calls made from a
	// request handler (probe, reboot) so a hung Manager cannot hold a request open.
	nodepackProbeTimeout = 15 * time.Second
)

// nodepackJob is the in-memory state of a single background node-pack install.
// All fields are read/written only under Server.nodepackMu.
type nodepackJob struct {
	running bool
	seq     int64
	// title / manualCmd are captured at start so the terminal fragment can name the
	// pack and fall back to the manual command without re-deriving anything.
	title     string
	manualCmd string
	// lines is the append-only progress log. It is appended to AND snapshotted
	// under nodepackMu (the repo's streaming-job invariant).
	lines []string
	// message is the current status line, or the terminal outcome.
	message string
	// installed is true ONLY when ComfyUI-Manager's own installed-set diff showed
	// the pack. It is what the UI keys "success" on — never the HTTP status of the
	// install request.
	installed  bool
	finishedAt time.Time
}

// nodepackSnapshot is a locked, self-consistent view of the install job.
type nodepackSnapshot struct {
	Started, Running bool
	Seq              int64
	Title            string
	Message          string
	ManualCmd        string
	Installed        bool
	Lines            []string
}

// nodepackJobState returns a locked snapshot of the current install job. The
// progress lines are COPIED under the mutex so a concurrent append can never race
// a render.
func (s *Server) nodepackJobState() nodepackSnapshot {
	s.nodepackMu.Lock()
	defer s.nodepackMu.Unlock()
	j := s.nodepackJob
	if j == nil {
		return nodepackSnapshot{}
	}
	lines := make([]string, len(j.lines))
	copy(lines, j.lines)
	return nodepackSnapshot{
		Started: true, Running: j.running, Seq: j.seq, Title: j.title,
		Message: j.message, ManualCmd: j.manualCmd, Installed: j.installed,
		Lines: lines,
	}
}

// nodepackAppend appends a progress line and sets the current status message,
// both under nodepackMu.
func (s *Server) nodepackAppend(job *nodepackJob, line string) {
	s.nodepackMu.Lock()
	job.lines = append(job.lines, line)
	job.message = line
	s.nodepackMu.Unlock()
}

// nodepackSettle marks the job finished with its terminal outcome.
func (s *Server) nodepackSettle(job *nodepackJob, installed bool, msg string) {
	s.nodepackMu.Lock()
	job.running, job.installed, job.message, job.finishedAt = false, installed, msg, time.Now()
	s.nodepackMu.Unlock()
}

// handleWorkflowNodepackInstall drives the two-click, per-pack gated install of
// ONE custom-node pack. CSRF-protected + loopback-gated (it reaches ComfyUI).
//
// The pack is NEVER taken from the request. The client sends only an (id,
// repository) pair identifying which of the CURRENT run's attributed packs it
// means; the server re-derives the pack from its own attribution snapshot and
// refuses anything it did not itself attribute. That is what stops a forged POST
// from pointing ComfyUI-Manager at an arbitrary repository.
//
// The first click (no confirm) renders a confirmation naming the pack and showing
// its repository URL — nothing runs. Only a second click carrying confirm=1
// starts the install. There is no "install all" and no transitive install.
func (s *Server) handleWorkflowNodepackInstall(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	wfID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	pack, ok := s.attributedPack(r.FormValue("pack_id"), r.FormValue("pack_repo"))
	if !ok {
		// The attribution this click referred to is gone (a newer run replaced it, or
		// the server restarted). Re-running is the honest instruction — we will not
		// install a pack we cannot currently vouch for.
		s.render(w, http.StatusOK, nodepackNote("warning",
			"This install offer is out of date — the run it belonged to has been replaced. "+
				"Run the workflow again to get a fresh list of missing node packs."))
		return
	}
	if !pack.Installable {
		s.render(w, http.StatusOK, nodepackNote("warning", packBlockedReason(pack)))
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != "1" {
		s.render(w, http.StatusOK, nodepackConfirmFragment(pack, wfID, s.csrf))
		return
	}
	if started, msg := s.startNodepackInstall(pack); !started {
		s.render(w, http.StatusOK, nodepackNote("warning", msg))
		return
	}
	s.render(w, http.StatusOK, nodepackStatusFragment(s.nodepackJobState(), s.csrf))
}

// handleWorkflowNodepackStatus is polled by the install's running fragment. GET
// (no state change, no CSRF); loopback-gated like the other comfy-reaching
// endpoints.
func (s *Server) handleWorkflowNodepackStatus(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	s.render(w, http.StatusOK, nodepackStatusFragment(s.nodepackJobState(), s.csrf))
}

// handleWorkflowNodepackRestart restarts ComfyUI through ComfyUI-Manager.
// CSRF-protected + loopback-gated.
//
// 🔴 It is ALWAYS an explicit user action and never automatic: Manager's reboot
// is os.execv with no queue inspection at all, so it destroys a running
// generation rather than declining. comfy.ManagerReboot refuses (ErrQueueBusy)
// when ComfyUI's queue is non-empty or unreadable, and that refusal is surfaced
// here as its own plain message — never as a generic error.
func (s *Server) handleWorkflowNodepackRestart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	if snap := s.nodepackJobState(); snap.Running {
		s.render(w, http.StatusOK, nodepackNote("warning",
			"An install is still running — wait for it to finish before restarting ComfyUI."))
		return
	}
	mc := s.managerClient()
	if mc == nil {
		s.render(w, http.StatusOK, nodepackNote("warning",
			"Local ComfyUI is not configured, so it cannot be restarted from here."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nodepackProbeTimeout)
	defer cancel()

	info, err := mc.ManagerProbe(ctx)
	if err != nil || info == nil || !info.Present {
		s.render(w, http.StatusOK, nodepackNote("warning",
			"ComfyUI-Manager's web API did not answer, so ComfyUI cannot be restarted from here. "+
				"Restart it yourself."))
		return
	}
	switch err := mc.ManagerReboot(ctx, info); {
	case err == nil:
		s.render(w, http.StatusOK, nodepackNote("success",
			"Restart requested. ComfyUI is coming back up — give it a few seconds, "+
				"then run the workflow again."))
	case errors.Is(err, comfy.ErrQueueBusy):
		// The refusal is OURS, and it is the useful message — say exactly that.
		s.render(w, http.StatusOK, nodepackNote("warning",
			"ComfyUI is busy: a generation is running or queued — wait for it to finish "+
				"or cancel it first, then restart."))
	default:
		s.render(w, http.StatusOK, nodepackNote("error",
			"ComfyUI-Manager refused the restart: "+err.Error()+
				". Restart ComfyUI yourself instead."))
	}
}

// attributedPack re-derives ONE pack from the current run's attribution snapshot,
// matching the client-supplied (id, repository) pair. Both must match a pack we
// actually attributed; nothing about the pack (least of all its repository URL)
// is ever taken from the request.
func (s *Server) attributedPack(id, repo string) (comfy.Pack, bool) {
	id, repo = strings.TrimSpace(id), strings.TrimSpace(repo)
	if id == "" && repo == "" {
		return comfy.Pack{}, false
	}
	for _, p := range s.runJobState().NodeAttr.Packs {
		if strings.TrimSpace(p.ID) == id && strings.TrimSpace(p.Repository) == repo {
			return p, true
		}
	}
	return comfy.Pack{}, false
}

// startNodepackInstall launches the background install unless one is already
// running. It returns (false, message) when it declined, so the caller can render
// the reason instead of silently doing nothing.
func (s *Server) startNodepackInstall(pack comfy.Pack) (bool, string) {
	s.nodepackMu.Lock()
	if s.nodepackJob != nil && s.nodepackJob.running {
		s.nodepackMu.Unlock()
		return false, "Another node-pack install is already running. Wait for it to finish."
	}
	manual, _ := manualInstallCommand(pack, s.cfg.ComfyRoot)
	s.nodepackSeq++
	job := &nodepackJob{
		running: true, seq: s.nodepackSeq,
		title:     packDisplayTitle(pack),
		manualCmd: manual,
		message:   "Asking ComfyUI-Manager to install this pack…",
	}
	s.nodepackJob = job
	s.nodepackMu.Unlock()

	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, nodepackInstallBudget)
	go func() {
		defer cancel()
		// A wedged/hostile Manager response must fail THIS job, not crash the server.
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("node-pack install panicked", "err", rec)
				s.nodepackSettle(job, false,
					"The install failed unexpectedly. Install the pack by hand instead.")
			}
		}()
		s.runNodepackInstall(ctx, job, pack)
	}()
	return true, ""
}

// runNodepackInstall is the install job body: delegate to ComfyUI-Manager, wait
// for its queue to go idle, then CONFIRM against the installed-set diff. It never
// reports a success it did not observe.
func (s *Server) runNodepackInstall(ctx context.Context, job *nodepackJob, pack comfy.Pack) {
	mc := s.managerClient()
	if mc == nil {
		s.nodepackSettle(job, false,
			"Local ComfyUI is not configured, so nothing could be installed.")
		return
	}
	info, err := mc.ManagerProbe(ctx)
	if err != nil || info == nil || !info.Present {
		s.nodepackSettle(job, false,
			"ComfyUI-Manager's web API did not answer, so nothing was installed.")
		return
	}
	s.nodepackAppend(job, "ComfyUI-Manager "+managerLabel(info)+" is answering.")

	// Baseline the "installed but not yet imported" set FIRST. Without it a pack
	// that was already sitting there pending a restart would read as "we just
	// installed it".
	baseline := map[string]struct{}{}
	if before, dErr := mc.ManagerInstalledDiff(ctx); dErr == nil {
		for _, name := range before {
			baseline[name] = struct{}{}
		}
	} else {
		s.log.Warn("read installed baseline failed", "err", dErr)
	}
	if _, already := matchPackInDiff(pack, keysOf(baseline)); already {
		s.nodepackSettle(job, true,
			"This pack is already installed and waiting for a restart — ComfyUI must restart "+
				"before this workflow can run.")
		return
	}

	s.nodepackAppend(job, "Asking ComfyUI-Manager to install "+packDisplayTitle(pack)+"…")
	if err := mc.ManagerInstall(ctx, info, pack); err != nil {
		s.nodepackSettle(job, false, explainManagerInstallError(err))
		return
	}
	s.nodepackAppend(job, "Queued with ComfyUI-Manager. Waiting for it to finish…")

	confirmed := s.waitForNodepackInstall(ctx, mc, info, job, pack, baseline)
	if confirmed {
		s.nodepackSettle(job, true,
			packDisplayTitle(pack)+" installed — ComfyUI must restart before this workflow can run.")
		return
	}
	s.nodepackSettle(job, false,
		"ComfyUI-Manager finished, but "+packDisplayTitle(pack)+" does not show up as installed. "+
			"It may have been refused or it may have failed — check ComfyUI's console. "+
			"Nothing here can confirm it landed.")
}

// waitForNodepackInstall polls Manager's queue for progress and its installed-set
// diff for the actual answer, returning whether the pack demonstrably landed.
//
// The diff is checked on EVERY tick, not only at the end, so a fast install is
// confirmed immediately. The queue counters are progress only: is_processing is
// false before a queue starts, so an all-idle read is believed only after
// nodepackMinSettle (or after we actually saw work in flight).
func (s *Server) waitForNodepackInstall(ctx context.Context, mc managerClient, info *comfy.ManagerInfo, job *nodepackJob, pack comfy.Pack, baseline map[string]struct{}) bool {
	start := time.Now()
	sawBusy := false
	lastProgress := ""

	for {
		if pending, dErr := mc.ManagerInstalledDiff(ctx); dErr == nil {
			if name, ok := matchPackInDiff(pack, newNames(pending, baseline)); ok {
				s.nodepackAppend(job, "ComfyUI-Manager reports "+name+" is installed and pending a restart.")
				return true
			}
		}

		busy := false
		if st, sErr := mc.ManagerQueueStatus(ctx, info); sErr == nil && st != nil {
			busy = st.IsProcessing || (st.TotalCount > 0 && st.DoneCount < st.TotalCount)
			if st.TotalCount > 0 {
				line := fmt.Sprintf("ComfyUI-Manager: %d of %d task(s) done.", st.DoneCount, st.TotalCount)
				if line != lastProgress {
					lastProgress = line
					s.nodepackAppend(job, line)
				}
			}
		}
		if busy {
			sawBusy = true
		} else if sawBusy || time.Since(start) >= s.nodepackSettleWait {
			return false
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(s.nodepackPoll):
		}
	}
}

// explainManagerInstallError turns a ComfyUI-Manager refusal into something the
// user can act on. Manager answers a policy refusal with a terse 403/404 body,
// which as a bare error reads as an opaque failure; naming the policy is what
// makes it actionable. The underlying text is included verbatim (and escaped at
// render) so nothing is hidden.
func explainManagerInstallError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "403") || strings.Contains(msg, "404") {
		return "ComfyUI-Manager refused the install (" + msg + "). Its security policy blocks " +
			"installs it considers unsafe — typically a pack with no Comfy Registry release, " +
			"which it can only fetch as a raw git clone. Install it by hand instead, or allow " +
			"git-url installs in ComfyUI-Manager's settings."
	}
	return "ComfyUI-Manager could not install this pack: " + msg + ". Install it by hand instead."
}

// managerLabel names the detected Manager for a progress line.
func managerLabel(info *comfy.ManagerInfo) string {
	if info == nil {
		return ""
	}
	if v := strings.TrimSpace(info.Version); v != "" {
		return v
	}
	return info.Line
}

// matchPackInDiff reports whether one of ComfyUI-Manager's installed-set entries
// is this pack. Entries are custom_nodes directory names / pack ids, so a pack is
// recognised by its id or by the last path segment of its repository URL — the
// two names a Manager install can land under.
func matchPackInDiff(pack comfy.Pack, names []string) (string, bool) {
	want := map[string]struct{}{}
	for _, c := range []string{pack.ID, repoLastSegment(pack.Repository)} {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			want[c] = struct{}{}
		}
	}
	for _, n := range names {
		if _, ok := want[strings.ToLower(strings.TrimSpace(n))]; ok {
			return n, true
		}
	}
	return "", false
}

// repoLastSegment is the repository URL's final path segment without a .git
// suffix — the directory name a git clone produces.
func repoLastSegment(repo string) string {
	repo = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(repo), "/"), ".git")
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return ""
}

// newNames returns the entries of pending that were not already in baseline.
func newNames(pending []string, baseline map[string]struct{}) []string {
	out := make([]string, 0, len(pending))
	for _, n := range pending {
		if _, old := baseline[n]; !old {
			out = append(out, n)
		}
	}
	return out
}

// keysOf flattens a set to a slice.
func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}
