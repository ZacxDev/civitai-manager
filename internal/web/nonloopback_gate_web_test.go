package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

const gateMsg = "disabled when the server is bound to a non-loopback"

// countingSubscriber records SubscribeModel/SubscribeCreator calls so a test can
// assert a gated endpoint performed NO subscription write.
type countingSubscriber struct{ modelCalls, creatorCalls int }

func (c *countingSubscriber) SubscribeModel(context.Context, int, poller.SubscribeOptions) (int64, error) {
	c.modelCalls++
	return 1, nil
}
func (c *countingSubscriber) SubscribeCreator(context.Context, string, poller.SubscribeOptions) (int64, error) {
	c.creatorCalls++
	return 1, nil
}

// gateTestServer builds a server bound to addr with the download-capable reader
// and a counting subscriber (returned for call-count assertions).
func gateTestServer(t *testing.T, addr string) (*Server, *countingSubscriber) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sub := &countingSubscriber{}
	srv := NewServer(st, downloadReader(t), sub, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: addr, ModelRoot: "/models",
	}, nil)
	return srv, sub
}

// TestNonLoopbackGatesStateChangingEndpoints proves the security-audit v0.1.64 🟡-3
// fix: on a non-loopback bind the destructive/egress endpoints (quarantine,
// download, subscribe/unsubscribe/subscribe) return the gated notice and perform NO
// side effect, even with a valid CSRF token (CSRF is not an auth boundary).
func TestNonLoopbackGatesStateChangingEndpoints(t *testing.T) {
	srv, sub := gateTestServer(t, "0.0.0.0:8787") // non-loopback: LAN-exposed

	// Pre-seed a model subscription so unsubscribe has something it COULD delete;
	// the gate must leave it intact.
	mid := 7
	if _, err := srv.store.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"download", "/models/7/download", downloadForm("11", "1")},
		{"quarantine", "/library/quarantine", url.Values{"ids": {"1,2,3"}, "apply": {"true"}}},
		{"model-subscribe", "/models/7/subscribe", url.Values{}},
		{"model-unsubscribe", "/models/7/unsubscribe", url.Values{}},
		{"subscribe", "/subscribe", url.Values{"model": {"7"}}},
	}
	for _, c := range cases {
		rec := post(t, srv, c.path, c.form, true) // valid CSRF
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (gated notice)", c.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), gateMsg) {
			t.Errorf("%s: expected gated notice, got:\n%s", c.name, rec.Body.String())
		}
	}

	// No side effects: no queued download, no subscribe call, the seeded
	// subscription survives (unsubscribe never ran).
	if items, _ := srv.store.ListQueue(); len(items) != 0 {
		t.Errorf("gated download must not enqueue, got %d", len(items))
	}
	if sub.modelCalls != 0 || sub.creatorCalls != 0 {
		t.Errorf("gated subscribe must not call subscriber, got model=%d creator=%d", sub.modelCalls, sub.creatorCalls)
	}
	if subs, _ := srv.store.ListSubscriptions(); len(subs) != 1 {
		t.Errorf("gated unsubscribe must not delete; want 1 subscription, got %d", len(subs))
	}
}

// TestLoopbackStateChangingEndpointsStillWork proves the DEFAULT loopback bind is
// unaffected by the 🟡-3 gate: download enqueues and subscribe reaches the
// subscriber.
func TestLoopbackStateChangingEndpointsStillWork(t *testing.T) {
	srv, sub := gateTestServer(t, "127.0.0.1:8787") // default loopback

	rec := post(t, srv, "/models/7/download", downloadForm("11", "1"), true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Queued") {
		t.Fatalf("loopback download should enqueue, got %d:\n%s", rec.Code, rec.Body.String())
	}
	if items, _ := srv.store.ListQueue(); len(items) != 1 {
		t.Fatalf("loopback download should enqueue exactly one item, got %d", len(items))
	}

	rec = post(t, srv, "/subscribe", url.Values{"model": {"7"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback subscribe status = %d", rec.Code)
	}
	if sub.modelCalls != 1 {
		t.Fatalf("loopback subscribe should reach the subscriber once, got %d", sub.modelCalls)
	}
	// The gated notice must NOT appear on a loopback bind.
	if strings.Contains(rec.Body.String(), gateMsg) {
		t.Error("loopback subscribe must not render the gated notice")
	}
}
