package comfy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubmitCloud_WhatifRequestAndResponse(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		gotAuth   string
		gotBody   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("whatif")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{
			"id":"wf-123","status":"unassigned",
			"cost":{"base":42.5,"factors":{"priority":1.2}},
			"transactions":{"insufficientBuzz":false}
		}`)
	}))
	defer srv.Close()

	c := NewCloudClient(srv.URL, "tok-abc")
	graph := json.RawMessage(`{"3":{"class_type":"KSampler","inputs":{}}}`)
	tmpl := NewCustomComfyTemplate(graph, []string{"urn:air:sdxl:checkpoint:civitai:1@2"})

	wf, err := c.SubmitCloud(context.Background(), tmpl, true)
	if err != nil {
		t.Fatalf("SubmitCloud: %v", err)
	}

	// Request assertions.
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/v2/consumer/workflows" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "true" {
		t.Errorf("whatif query = %q, want true", gotQuery)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
	// Body shape: steps[0].$type == customComfy, input.resources, input.trace, input.workflow present.
	steps, _ := gotBody["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("steps len = %d, want 1", len(steps))
	}
	step0, _ := steps[0].(map[string]any)
	if step0["$type"] != "customComfy" {
		t.Errorf("$type = %v, want customComfy", step0["$type"])
	}
	input, _ := step0["input"].(map[string]any)
	if input == nil {
		t.Fatalf("input missing")
	}
	if input["trace"] != "none" {
		t.Errorf("trace = %v, want none", input["trace"])
	}
	res, _ := input["resources"].([]any)
	if len(res) != 1 || res[0] != "urn:air:sdxl:checkpoint:civitai:1@2" {
		t.Errorf("resources = %v", res)
	}
	if _, ok := input["workflow"]; !ok {
		t.Errorf("input.workflow missing")
	}

	// Response decoding.
	if wf.ID != "wf-123" {
		t.Errorf("id = %q", wf.ID)
	}
	if wf.BaseCost() != 42.5 {
		t.Errorf("base cost = %v, want 42.5", wf.BaseCost())
	}
	if wf.InsufficientBuzz() {
		t.Errorf("insufficientBuzz = true, want false")
	}
}

func TestSubmitCloud_InsufficientBuzz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"x","status":"unassigned","cost":{"base":9000},"transactions":{"insufficientBuzz":true}}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "")
	wf, err := c.SubmitCloud(context.Background(), NewCustomComfyTemplate(json.RawMessage(`{}`), nil), true)
	if err != nil {
		t.Fatalf("SubmitCloud: %v", err)
	}
	if !wf.InsufficientBuzz() {
		t.Errorf("insufficientBuzz = false, want true")
	}
	if wf.BaseCost() != 9000 {
		t.Errorf("base = %v", wf.BaseCost())
	}
}

func TestSubmitCloud_NoAuthHeaderWhenNoToken(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		io.WriteString(w, `{"id":"x","status":"unassigned"}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "")
	if _, err := c.SubmitCloud(context.Background(), NewCustomComfyTemplate(json.RawMessage(`{}`), nil), false); err != nil {
		t.Fatalf("SubmitCloud: %v", err)
	}
	if hadAuth {
		t.Errorf("Authorization header sent with empty token")
	}
}

func TestSubmitCloud_202Accepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"wf-async","status":"processing"}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "t")
	wf, err := c.SubmitCloud(context.Background(), NewCustomComfyTemplate(json.RawMessage(`{}`), nil), false)
	if err != nil {
		t.Fatalf("SubmitCloud (202): %v", err)
	}
	if wf.ID != "wf-async" || wf.Status != "processing" {
		t.Errorf("wf = %+v", wf)
	}
}

func TestSubmitCloud_ProblemDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"title":"Bad Request","detail":"resource urn:air:foo not found","status":400}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "t")
	_, err := c.SubmitCloud(context.Background(), NewCustomComfyTemplate(json.RawMessage(`{}`), nil), false)
	if err == nil {
		t.Fatal("expected error")
	}
	var p *CloudProblem
	if !asCloudProblem(err, &p) {
		t.Fatalf("error is not *CloudProblem: %T %v", err, err)
	}
	if p.StatusCode != 400 || p.Title != "Bad Request" || !strings.Contains(p.Detail, "not found") {
		t.Errorf("problem = %+v", p)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error string = %q", err.Error())
	}
}

func TestSubmitCloud_ForbiddenBareString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `"Forbidden"`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "t")
	_, err := c.SubmitCloud(context.Background(), NewCustomComfyTemplate(json.RawMessage(`{}`), nil), false)
	var p *CloudProblem
	if !asCloudProblem(err, &p) {
		t.Fatalf("not a CloudProblem: %v", err)
	}
	if p.StatusCode != 403 || p.Detail != "Forbidden" {
		t.Errorf("problem = %+v", p)
	}
}

func TestGetCloudWorkflow_CompletedBlobs(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{
			"id":"wf-9","status":"succeeded",
			"steps":[{"name":"comfy","status":"succeeded","output":{"blobs":[
				{"id":"b1","url":"https://image.civitai.com/a.jpeg","available":true,"type":"image"},
				{"id":"b2","url":"","available":false,"type":"image"}
			]}}]
		}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "t")
	wf, err := c.GetCloudWorkflow(context.Background(), "wf-9")
	if err != nil {
		t.Fatalf("GetCloudWorkflow: %v", err)
	}
	if gotPath != "/v2/consumer/workflows/wf-9" {
		t.Errorf("path = %q", gotPath)
	}
	if !CloudStatusTerminal(wf.Status) {
		t.Errorf("status %q should be terminal", wf.Status)
	}
	urls := wf.BlobURLs()
	if len(urls) != 1 || urls[0] != "https://image.civitai.com/a.jpeg" {
		t.Errorf("blob urls = %v (blank url should be skipped)", urls)
	}
}

func TestGetCloudWorkflow_Problem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"title":"Unauthorized","detail":"bad token","status":401}`)
	}))
	defer srv.Close()
	c := NewCloudClient(srv.URL, "t")
	_, err := c.GetCloudWorkflow(context.Background(), "x")
	var p *CloudProblem
	if !asCloudProblem(err, &p) || p.StatusCode != 401 {
		t.Fatalf("want 401 CloudProblem, got %v", err)
	}
}

func TestCloudStatusTerminal(t *testing.T) {
	terminal := []string{"succeeded", "failed", "expired", "canceled", "SUCCEEDED"}
	nonTerminal := []string{"unassigned", "preparing", "scheduled", "processing", ""}
	for _, s := range terminal {
		if !CloudStatusTerminal(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if CloudStatusTerminal(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

func TestNewCloudClient_DefaultBaseURL(t *testing.T) {
	if got := NewCloudClient("", "t").BaseURL(); got != DefaultCloudBaseURL {
		t.Errorf("default base = %q", got)
	}
	if got := NewCloudClient("https://x.example/", "t").BaseURL(); got != "https://x.example" {
		t.Errorf("trimmed base = %q", got)
	}
}

// asCloudProblem is a tiny errors.As helper kept local to avoid importing errors
// in the assertions above repeatedly.
func asCloudProblem(err error, target **CloudProblem) bool {
	if p, ok := err.(*CloudProblem); ok {
		*target = p
		return true
	}
	return false
}
