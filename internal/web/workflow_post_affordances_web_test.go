package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// workflowPostReader returns the standard model fixture retyped as a CivitAI
// Workflows post, with its version's files retyped as the Archive .zip a real
// workflow post ships.
//
// THE FIXTURE IS THE POINT. A workflow post is NOT distinguishable from a
// checkpoint by absence (measured 2026-07-31 on 1847730 / 1386234): the version
// keeps a populated baseModel, every file keeps a real downloadUrl, a SHA256 and
// a sizeKB, and the primary flag is set. Only `type` differs. A fixture that
// dropped any of those would let the assertions below pass because the page had
// no files to offer, which is a different bug from the one under test.
func workflowPostReader(t *testing.T) fakeReader {
	t.Helper()
	fr := newModelReader(t)

	m := *fr.model
	m.Type = "Workflows"
	m.Name = "WAN 2.2 AIO workflow"
	fr.model = &m

	v := *fr.version
	v.Files = []civitai.ModelVersionFile{{
		ID: 1, Name: "WAN22_AIO.zip", Type: "Archive", SizeKB: 812.5, Primary: true,
		DownloadURL: "https://civitai.com/api/download/models/11",
		Hashes:      civitai.FileHashes{SHA256: "ZIPHASH"},
	}}
	fr.version = &v
	return fr
}

// assertWorkflowFixtureIsDownloadableWhole pins the intermediate state: this
// fixture really would sail through the download path if nothing stopped it.
func assertWorkflowFixtureIsDownloadableWhole(t *testing.T, fr fakeReader) {
	t.Helper()
	if !civitai.IsWorkflowPost(fr.model.Type) {
		t.Fatalf("fixture is not a workflow post: type=%q", fr.model.Type)
	}
	f := civitai.SelectFile(fr.version.Files, "")
	if f == nil {
		t.Fatal("fixture does not reach the interesting case: SelectFile found no file, so the " +
			"download control would be absent for a reason unrelated to the type")
	}
	if f.DownloadURL == "" || f.Hashes.SHA256 == "" || f.SizeKB == 0 || !f.Primary {
		t.Fatalf("fixture does not reach the interesting case: a workflow post's file carries "+
			"downloadUrl/SHA256/sizeKB/primary exactly like a checkpoint's, got %+v", f)
	}
	if fr.version.BaseModel == "" {
		t.Fatal("fixture does not reach the interesting case: a workflow post carries a populated baseModel")
	}
}

// TestWorkflowPostDetailPageOffersImportNotDownload is 🟡-3 + 🔴-1(b) on the
// detail page: the header's primary action is repointed from Download to the
// import card, and the subscribe control becomes notify-only. /models/1847730
// used to emit a `>Download<` posting to /models/1847730/download that was
// markup-identical to a real checkpoint's.
func TestWorkflowPostDetailPageOffersImportNotDownload(t *testing.T) {
	fr := workflowPostReader(t)
	assertWorkflowFixtureIsDownloadableWhole(t, fr)
	srv := newModelServer(t, fr)
	body := getModelPage(t, srv, "/models/7")

	if strings.Contains(body, `hx-post="/models/7/download"`) {
		t.Errorf("a workflow post's page must not offer the download POST at all:\n%s", body)
	}
	// The repointed action, and the anchor it targets.
	if !strings.Contains(body, `href="#`+workflowImportCardID+`"`) {
		t.Errorf("the header's primary action should link to the import card:\n%s", body)
	}
	if !strings.Contains(body, `id="`+workflowImportCardID+`"`) {
		t.Errorf("the import card must carry the anchor the header links to:\n%s", body)
	}
	// Subscribe became notify-only.
	if !strings.Contains(body, ">Notify me<") {
		t.Errorf("a workflow post should offer 'Notify me', not a plain Subscribe:\n%s", body)
	}
	if strings.Contains(body, ">Subscribe<") {
		t.Errorf("a workflow post must not offer a plain 'Subscribe' button:\n%s", body)
	}
	if !strings.Contains(body, "/models/7/subscribe-options?"+workflowParamName+"=1") {
		t.Errorf("the control should carry the workflow flag across the htmx swap:\n%s", body)
	}
}

// TestRealModelDetailPageIsUnchanged is the both-directions half on the detail
// page: a Checkpoint keeps its Download button, its plain Subscribe, and gains
// none of the workflow wording. This is the regression the fix invites.
func TestRealModelDetailPageIsUnchanged(t *testing.T) {
	srv := newModelServer(t, newModelReader(t)) // Checkpoint
	body := getModelPage(t, srv, "/models/7")

	if !strings.Contains(body, `hx-post="/models/7/download"`) {
		t.Errorf("a Checkpoint must keep its download control:\n%s", body)
	}
	if !strings.Contains(body, ">Subscribe<") {
		t.Errorf("a Checkpoint must keep its plain Subscribe button:\n%s", body)
	}
	for _, banned := range []string{
		">Notify me<",
		"href=\"#" + workflowImportCardID + "\"",
		workflowParamName + "=1",
		"imported, not downloaded",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("a Checkpoint page must not carry %q:\n%s", banned, body)
		}
	}
}

// TestSearchCardOffersNotifyOnlyForAWorkflowPost is 🔴-1(b) on the card surface.
// It is the likeliest place to meet one: /search sets no `types` param (correctly
// — mixing workflow posts into keyword results is not the bug), and
// `query="wan workflow"` returns 98 of 99 items as type "Workflows". A card only
// has the LIST type, which is why the predicate takes a string.
func TestSearchCardOffersNotifyOnlyForAWorkflowPost(t *testing.T) {
	subs := map[int]*store.Subscription{}
	wf := civitai.ModelListItem{ID: 1847730, Name: "WAN 2.2 AIO workflow", Type: "Workflows"}
	real := civitai.ModelListItem{ID: 4384, Name: "DreamShaper", Type: "Checkpoint"}

	wfOut := renderString(t, modelCardWith(wf, nil, subs, fullMaturityRange(), "csrf", modelUpdateInfo{}))
	if !strings.Contains(wfOut, ">Notify me<") {
		t.Errorf("a workflow-post card should offer 'Notify me':\n%s", wfOut)
	}
	if strings.Contains(wfOut, ">Subscribe<") {
		t.Errorf("a workflow-post card must not offer a plain 'Subscribe':\n%s", wfOut)
	}
	if !strings.Contains(wfOut, "/models/1847730/subscribe-options?"+workflowParamName+"=1") {
		t.Errorf("a workflow-post card must carry the workflow flag into the options request:\n%s", wfOut)
	}

	realOut := renderString(t, modelCardWith(real, nil, subs, fullMaturityRange(), "csrf", modelUpdateInfo{}))
	if !strings.Contains(realOut, ">Subscribe<") {
		t.Errorf("a Checkpoint card must keep its plain 'Subscribe':\n%s", realOut)
	}
	if strings.Contains(realOut, ">Notify me<") || strings.Contains(realOut, workflowParamName+"=1") {
		t.Errorf("a Checkpoint card must be entirely unaffected:\n%s", realOut)
	}
	if !strings.Contains(realOut, "/models/4384/subscribe-options\"") {
		t.Errorf("a Checkpoint card's options request must stay unqualified:\n%s", realOut)
	}
}

// TestWorkflowPostSubscribeOptionsPanelIsNotifyOnly proves the panel a workflow
// post's control opens offers NO auto-download choice and posts notify_only.
// Auto-download there would resolve to a permanent poller skip, so offering it
// would be an option that cannot do anything.
func TestWorkflowPostSubscribeOptionsPanelIsNotifyOnly(t *testing.T) {
	srv := newSubscribeServer(t)
	rec := get(t, srv, "/models/7/subscribe-options?"+workflowParamName+"=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("options = %d", rec.Code)
	}
	out := rec.Body.String()
	if strings.Contains(out, `value="auto_download"`) {
		t.Errorf("a workflow post's options panel must not offer Auto-download:\n%s", out)
	}
	if !strings.Contains(out, `type="hidden" name="mode" value="notify_only"`) {
		t.Errorf("a workflow post's options panel must post mode=notify_only:\n%s", out)
	}
	if !strings.Contains(out, "imported, not downloaded") {
		t.Errorf("the panel must say why there is no download choice:\n%s", out)
	}
	// Cancel must return to the workflow-shaped collapsed control, not a plain one.
	if !strings.Contains(out, "/models/7/subscribe-control?"+workflowParamName+"=1") {
		t.Errorf("Cancel must carry the workflow flag back:\n%s", out)
	}

	// Unflagged, the SAME endpoint still renders the ordinary two-mode panel.
	plain := get(t, srv, "/models/7/subscribe-options").Body.String()
	if !strings.Contains(plain, `value="auto_download"`) || !strings.Contains(plain, `value="notify_only"`) {
		t.Errorf("a normal model's options panel must still offer both modes:\n%s", plain)
	}
}

// TestWorkflowPostSubscribeIsStoredAsNotifyOnly proves the coercion is on the
// SERVER, not only in the markup: a hand-crafted mode=auto_download POST for a
// workflow post still persists a notify-only subscription, so the control can
// never render "Subscribed ✓ · auto-download" about something the poller will
// always skip.
//
// Both directions run against ONE server on purpose: store.Open(":memory:")
// resolves to `file::memory:?cache=shared`, so two stores opened while the first
// is still alive are the SAME database (the trap that made 15 test servers share
// one DB). Two model ids on one server is the isolation that actually holds.
func TestWorkflowPostSubscribeIsStoredAsNotifyOnly(t *testing.T) {
	srv := newSubscribeServer(t)

	// 7 = the workflow post. A hand-crafted auto_download is coerced.
	form := url.Values{"mode": {"auto_download"}, workflowParamName: {"1"}}
	rec := post(t, srv, "/models/7/subscribe", form, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	sub := srv.modelSubscription(7)
	if sub == nil {
		t.Fatal("subscribing to a workflow post should still create a subscription")
	}
	if sub.AutoDownload || !sub.NotifyOnly {
		t.Fatalf("a workflow post's subscription must be notify-only, got %+v", sub)
	}
	if !strings.Contains(rec.Body.String(), "notify only") {
		t.Errorf("the feedback must say notify only:\n%s", rec.Body.String())
	}

	// 8 = an ordinary model. Entirely unaffected — this is the half that catches
	// a coercion applied too broadly.
	if rec := post(t, srv, "/models/8/subscribe", url.Values{"mode": {"auto_download"}}, true); rec.Code != http.StatusOK {
		t.Fatalf("subscribe = %d", rec.Code)
	}
	if sub := srv.modelSubscription(8); sub == nil || !sub.AutoDownload || sub.NotifyOnly {
		t.Fatalf("an ordinary model must still get an auto-download subscription, got %+v", sub)
	}
}

// TestModelIsWorkflowPostReadsTheModelCache proves the server-side half of the
// flag works with NO request hint at all — the path that covers a stale page or a
// hand-crafted POST, where there is nothing to trust.
func TestModelIsWorkflowPostReadsTheModelCache(t *testing.T) {
	srv := newSubscribeServer(t)
	// No cache entry → fails OPEN (false). That is deliberate: answering true on a
	// miss would silently narrow a REAL model's options.
	if srv.modelIsWorkflowPost(7) {
		t.Error("a cache miss must answer false (fail open)")
	}
	if err := srv.store.PutModelCache(7, "WAN 2.2 AIO", []byte(`{"id":7,"name":"WAN 2.2 AIO","type":"Workflows"}`)); err != nil {
		t.Fatal(err)
	}
	if !srv.modelIsWorkflowPost(7) {
		t.Error("a cached Workflows model must answer true with no request hint")
	}
	if err := srv.store.PutModelCache(8, "DreamShaper", []byte(`{"id":8,"name":"DreamShaper","type":"Checkpoint"}`)); err != nil {
		t.Fatal(err)
	}
	if srv.modelIsWorkflowPost(8) {
		t.Error("a cached Checkpoint must answer false")
	}
	// …and with the cache alone saying "workflow", the rendered control follows
	// even though the request carries no flag.
	out := get(t, srv, "/models/7/subscribe-control").Body.String()
	if !strings.Contains(out, ">Notify me<") {
		t.Errorf("the cache-derived flag must shape the control:\n%s", out)
	}
}

// TestModelDownloadRefusesAWorkflowPostServerSide is 🟡-3's second half. The
// button disappearing is not the same as the endpoint refusing: this POST is
// reachable from any loopback+CSRF caller and from a page rendered before the
// upgrade.
func TestModelDownloadRefusesAWorkflowPostServerSide(t *testing.T) {
	fr := workflowPostReader(t)
	assertWorkflowFixtureIsDownloadableWhole(t, fr)
	srv := newModelServer(t, fr)
	srv.cfg.ModelRoot = t.TempDir()

	rec := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "imported, not downloaded") {
		t.Errorf("the endpoint must say why it refused:\n%s", out)
	}
	if strings.Contains(out, "Queued") {
		t.Errorf("the endpoint must not report a queued download:\n%s", out)
	}
	items, err := srv.store.ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("a workflow post must create NO download_queue row, got %d: %+v", len(items), items)
	}
}

// TestModelDownloadStillEnqueuesARealModel is the both-directions half of the
// endpoint guard, and it is what would catch a guard written too broadly (e.g.
// keyed on the file being an Archive, or failing CLOSED on an absent type).
func TestModelDownloadStillEnqueuesARealModel(t *testing.T) {
	srv := newModelServer(t, newModelReader(t)) // Checkpoint
	srv.cfg.ModelRoot = t.TempDir()

	rec := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Queued") {
		t.Fatalf("a Checkpoint must still enqueue:\n%s", rec.Body.String())
	}
	items, _ := srv.store.ListQueue()
	if len(items) != 1 {
		t.Fatalf("a Checkpoint download should create exactly 1 queue row, got %d", len(items))
	}
}

// TestCreatorSubscribeOffersNotifyOnly is 🔴-2's second half: the creator path
// had auto_download=true as a HIDDEN input and no options panel, so "Notify only"
// was not reachable at all. It is a creator's models, not a single model, and a
// workflow-only creator is common — but this fixes the REAL-model half too, since
// the poller guard already handles the workflow downloads.
func TestCreatorSubscribeOffersNotifyOnly(t *testing.T) {
	out := renderString(t, subscribeCreatorInline("alice", "Subscribe to creator", "test-csrf"))
	for _, want := range []string{
		`value="auto_download"`,
		`value="notify_only"`,
		"Auto-download",
		"Notify only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("creator subscribe missing the %q option:\n%s", want, out)
		}
	}
	// Auto-download stays the DEFAULT, so the prior one-click behaviour is intact.
	if !strings.Contains(out, `value="auto_download" class="text-indigo-500" checked`) {
		t.Errorf("auto-download must stay the pre-selected default:\n%s", out)
	}
	if strings.Contains(out, `value="notify_only" class="text-indigo-500" checked`) {
		t.Errorf("notify-only must not be pre-checked (that would be a silent behaviour change):\n%s", out)
	}
}

// recordingSubscriber captures the SubscribeOptions each call receives. It
// deliberately touches no store: store.Open(":memory:") resolves to
// `file::memory:?cache=shared`, so a per-subtest store would be the SAME database
// as its siblings' and any count taken from it would be fictional.
type recordingSubscriber struct {
	last  *poller.SubscribeOptions
	calls *int
}

func (s recordingSubscriber) SubscribeModel(_ context.Context, _ int, opts poller.SubscribeOptions) (int64, error) {
	*s.last = opts
	*s.calls++
	return 1, nil
}

func (s recordingSubscriber) SubscribeCreator(_ context.Context, _ string, opts poller.SubscribeOptions) (int64, error) {
	*s.last = opts
	*s.calls++
	return 1, nil
}

// recordingSubscribeServer wires a Server to a recordingSubscriber and hands back
// the pointers the handler writes through.
func recordingSubscribeServer(t *testing.T) (*Server, *poller.SubscribeOptions, *int) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var last poller.SubscribeOptions
	calls := 0
	srv := NewServer(st, stubReader{}, recordingSubscriber{last: &last, calls: &calls}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "127.0.0.1:8787",
	}, nil)
	return srv, &last, &calls
}

// TestCreatorSubscribeNotifyOnlyReachesTheSubscriber proves the new radio is
// WIRED, not merely rendered: without handleSubscribe reading `mode`, the radios
// would render perfectly and change nothing at all -- the failure mode the markup
// assertion above cannot see.
func TestCreatorSubscribeNotifyOnlyReachesTheSubscriber(t *testing.T) {
	srv, last, calls := recordingSubscribeServer(t)

	for _, tc := range []struct {
		mode       string
		wantNotify bool
		wantAuto   bool
	}{
		{"notify_only", true, false},
		{"auto_download", false, true},
	} {
		before := *calls
		rec := post(t, srv, "/subscribe", url.Values{"creator": {"alice"}, "mode": {tc.mode}}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("mode=%s: subscribe = %d", tc.mode, rec.Code)
		}
		// Intermediate state: the handler really did reach the subscriber. Without
		// this the option assertions could pass over a call that never happened.
		if *calls != before+1 {
			t.Fatalf("mode=%s: subscriber was not called (calls %d -> %d)", tc.mode, before, *calls)
		}
		if last.NotifyOnly != tc.wantNotify || last.AutoDownload != tc.wantAuto {
			t.Errorf("mode=%s reached the subscriber as notify=%v auto=%v, want notify=%v auto=%v",
				tc.mode, last.NotifyOnly, last.AutoDownload, tc.wantNotify, tc.wantAuto)
		}
	}
}

// TestSubscribeDashboardCheckboxesStillWork guards the additive change to
// handleSubscribe: the dashboard form sends CHECKBOXES (auto_download=true /
// notify_only=true) and no `mode` at all. Reading `mode` must not have changed
// what those do.
func TestSubscribeDashboardCheckboxesStillWork(t *testing.T) {
	srv, last, calls := recordingSubscribeServer(t)

	for _, tc := range []struct {
		name       string
		form       url.Values
		wantAuto   bool
		wantNotify bool
	}{
		{"auto_download checkbox", url.Values{"model": {"7"}, "auto_download": {"true"}}, true, false},
		{"notify_only checkbox", url.Values{"model": {"7"}, "notify_only": {"true"}}, false, true},
		{"both", url.Values{"model": {"7"}, "auto_download": {"true"}, "notify_only": {"true"}}, true, true},
		{"neither", url.Values{"model": {"7"}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := *calls
			if rec := post(t, srv, "/subscribe", tc.form, true); rec.Code != http.StatusOK {
				t.Fatalf("subscribe = %d", rec.Code)
			}
			if *calls != before+1 {
				t.Fatalf("subscriber was not called (calls %d -> %d)", before, *calls)
			}
			if last.AutoDownload != tc.wantAuto || last.NotifyOnly != tc.wantNotify {
				t.Errorf("checkbox form %v reached the subscriber as auto=%v notify=%v, want auto=%v notify=%v",
					tc.form, last.AutoDownload, last.NotifyOnly, tc.wantAuto, tc.wantNotify)
			}
		})
	}
}

// TestWorkflowImportGateStillAcceptsAnEmptyType is the guard the brief asks for
// at the HTTP level: the Discover import endpoint must keep ACCEPTING a model
// whose cached detail carries NO type. 94 of the top 100 `types=Other` models
// ship an Archive zip and are genuinely not workflows, so the gate cannot be
// widened either -- and a future tightening of the empty case trips here rather
// than shipping (the same lesson as the raw-string type check that once refused
// legitimate LoCon installs).
//
// ONE server, distinct model ids per case: store.Open(":memory:") is
// `cache=shared`, so a per-subtest store would silently be the same database.
// The request carries HX-Request so the handler renders its answer instead of
// 303-redirecting with a flash.
func TestWorkflowImportGateStillAcceptsAnEmptyType(t *testing.T) {
	const notWorkflow = "not a Workflows-type model"
	srv := newSubscribeServer(t)
	// The import path answers "configure your token" BEFORE the type gate, so the
	// gate is unreachable without one. No egress happens: every fixture below has
	// an empty modelVersions[], which stops the handler one step later.
	srv.cfg.Token = "test-token"

	for _, tc := range []struct {
		name       string
		id         int
		raw        string
		wantReject bool
	}{
		// No `type` key at all -- the fail-OPEN case, and the one a tightening would
		// break. It must get PAST the type gate (it then stops on "no versions",
		// which is a different message).
		{"absent type", 91, `{"id":91,"name":"Mystery","modelVersions":[]}`, false},
		{"empty type", 92, `{"id":92,"name":"Mystery","type":"","modelVersions":[]}`, false},
		{"workflows", 93, `{"id":93,"name":"Pack","type":"Workflows","modelVersions":[]}`, false},
		// A model that positively says it is something else IS refused -- without
		// this half the "gate" could be deleted entirely and the test stay green.
		{"checkpoint", 94, `{"id":94,"name":"DreamShaper","type":"Checkpoint","modelVersions":[]}`, true},
		{"other", 95, `{"id":95,"name":"Node pack","type":"Other","modelVersions":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := srv.store.PutModelCache(tc.id, "M", []byte(tc.raw)); err != nil {
				t.Fatal(err)
			}
			rec := postHX(t, srv, "/workflows/discover/"+strconv.Itoa(tc.id)+"/import", url.Values{})
			if rec.Code != http.StatusOK {
				t.Fatalf("import = %d\n%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if got := strings.Contains(body, notWorkflow); got != tc.wantReject {
				t.Errorf("type %q: rejected-as-non-workflow = %v, want %v:\n%s",
					tc.name, got, tc.wantReject, body)
			}
			// The accepted cases must have got PAST the type gate rather than
			// failing earlier for an unrelated reason -- assert the intermediate
			// state, not just the absence of the rejection string.
			if !tc.wantReject && !strings.Contains(body, "no versions to import") {
				t.Errorf("type %q should have reached the versions check (proving it "+
					"passed the type gate), got:\n%s", tc.name, body)
			}
		})
	}
}

// postHX is post() with the HX-Request header, so a handler that
// POST-redirect-GETs a plain form submit renders its fragment instead.
func postHX(t *testing.T, srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", srv.csrf)
	req.Header.Set("HX-Request", "true")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}
