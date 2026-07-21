package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestSearchValuesFromFlags proves finding #3's query construction: each flag
// maps to the expected CivitAI models-search parameter, and unset flags are
// omitted.
func TestSearchValuesFromFlags(t *testing.T) {
	q := searchValues(searchOptions{
		query: "anime", tag: "style", username: "alice", kind: "LORA", limit: 5, nsfw: true,
	})
	want := map[string]string{
		"query": "anime", "tag": "style", "username": "alice",
		"types": "LORA", "limit": "5", "nsfw": "true",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("param %q = %q, want %q", k, got, v)
		}
	}

	// Unset flags must not appear.
	empty := searchValues(searchOptions{query: "solo"})
	for _, k := range []string{"tag", "username", "types", "limit", "nsfw"} {
		if empty.Has(k) {
			t.Errorf("param %q should be absent for an unset flag, got %q", k, empty.Get(k))
		}
	}
}

// TestSearchRunRendersTable proves the reader is called with the built values
// and the returned items are rendered in a readable table.
func TestSearchRunRendersTable(t *testing.T) {
	res := &civitai.ModelSearchResult{
		Items: []civitai.ModelListItem{
			{ID: 4201, Name: "Realistic Vision", Type: "Checkpoint",
				Creator: &civitai.Creator{Username: "bob"},
				Stats:   civitai.ModelStats{DownloadCount: 12345, ThumbsUpCount: 678}},
			{ID: 99, Name: "Spicy LoRA", Type: "LORA", NSFW: true,
				Creator: &civitai.Creator{Username: "carol"},
				Stats:   civitai.ModelStats{DownloadCount: 7, ThumbsUpCount: 1}},
		},
	}
	client := &cliFakeClient{search: res}

	var out bytes.Buffer
	opts := searchOptions{query: "vision", kind: "Checkpoint", limit: 10}
	if err := searchRun(context.Background(), client, &out, opts); err != nil {
		t.Fatalf("searchRun: %v", err)
	}

	// The reader received the flag-derived values.
	if client.lastSearch.Get("query") != "vision" || client.lastSearch.Get("types") != "Checkpoint" || client.lastSearch.Get("limit") != "10" {
		t.Errorf("SearchModels called with %v", client.lastSearch)
	}

	got := out.String()
	for _, want := range []string{"4201", "Realistic Vision", "Checkpoint", "bob", "12345", "678", "Spicy LoRA", "carol", "[NSFW]"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q; got:\n%s", want, got)
		}
	}
}

// TestSearchRunJSON proves --json emits the raw API body verbatim (valid JSON).
func TestSearchRunJSON(t *testing.T) {
	raw := []byte(`{"items":[{"id":1,"name":"X"}],"metadata":{}}`)
	client := &cliFakeClient{search: &civitai.ModelSearchResult{Raw: raw}}

	var out bytes.Buffer
	if err := searchRun(context.Background(), client, &out, searchOptions{query: "x", asJSON: true}); err != nil {
		t.Fatalf("searchRun: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &parsed); err != nil {
		t.Fatalf("--json output is not valid JSON: %v; got %q", err, out.String())
	}
	if _, ok := parsed["items"]; !ok {
		t.Errorf("--json output should carry items, got %q", out.String())
	}
}

// TestSearchRunEmpty renders a friendly message when there are no results.
func TestSearchRunEmpty(t *testing.T) {
	client := &cliFakeClient{search: &civitai.ModelSearchResult{}}
	var out bytes.Buffer
	if err := searchRun(context.Background(), client, &out, searchOptions{query: "nothing"}); err != nil {
		t.Fatalf("searchRun: %v", err)
	}
	if !strings.Contains(out.String(), "No models found") {
		t.Errorf("expected an empty-result message, got %q", out.String())
	}
}
