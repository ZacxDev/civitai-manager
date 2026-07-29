package config

import (
	"path/filepath"
	"testing"
)

// TestResolveNodePacksDefaultsOn covers the three states of the `resolve_node_packs`
// opt-out, mirroring hf_fallback: an ABSENT key means "not configured" and resolves
// ON (this is disclosed egress, not opt-in egress), and only an explicit `false`
// turns it off. The effective value is what String() reports, so a diagnostic log
// can never disagree with what the code actually does.
func TestResolveNodePacksDefaultsOn(t *testing.T) {
	cases := []struct {
		name string
		// body is the config file contents; empty means NO config file at all.
		body string
		want bool
	}{
		{name: "key absent — defaults ON", body: "", want: true},
		{name: "explicit false disables", body: "resolve_node_packs: false\n", want: false},
		{name: "explicit true enables", body: "resolve_node_packs: true\n", want: true},
		{
			name: "absent alongside other keys still defaults ON",
			body: "comfy_cloud: true\nhf_fallback: false\n",
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(EnvToken, "")
			t.Setenv(EnvComfyToken, "")

			path := filepath.Join(dir, "missing.yaml")
			if c.body != "" {
				path = writeConfig(t, dir, c.body)
			}
			cfg, err := Resolve(Flags{ConfigPath: path})
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ResolveNodePacksEnabled(); got != c.want {
				t.Errorf("ResolveNodePacksEnabled() = %v, want %v", got, c.want)
			}
			want := "ResolveNodePacks:true"
			if !c.want {
				want = "ResolveNodePacks:false"
			}
			if !contains(cfg.String(), want) {
				t.Errorf("String() should report %s, got %q", want, cfg.String())
			}
		})
	}
}

// TestResolveNodePacksIsIndependentOfHFFallback proves the two egress opt-outs do
// not share a knob: turning the HuggingFace fallback off must not silently disable
// custom-node attribution, and vice versa.
func TestResolveNodePacksIsIndependentOfHFFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvToken, "")
	t.Setenv(EnvComfyToken, "")

	cfgPath := writeConfig(t, dir, "hf_fallback: false\nresolve_node_packs: true\n")
	cfg, err := Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HFFallbackEnabled() {
		t.Error("hf_fallback: false should disable the HuggingFace fallback")
	}
	if !cfg.ResolveNodePacksEnabled() {
		t.Error("resolve_node_packs: true must stay on when hf_fallback is off")
	}

	cfgPath = writeConfig(t, dir, "hf_fallback: true\nresolve_node_packs: false\n")
	cfg, err = Resolve(Flags{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HFFallbackEnabled() {
		t.Error("hf_fallback: true must stay on when resolve_node_packs is off")
	}
	if cfg.ResolveNodePacksEnabled() {
		t.Error("resolve_node_packs: false should disable the online lookups")
	}
}
