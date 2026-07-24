package store

import (
	"reflect"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	st := newTestStore(t)

	// Unset key → default returned, present=false.
	if v, ok, err := st.GetSetting("nsfw_display"); err != nil || ok || v != "" {
		t.Fatalf("unset GetSetting = (%q,%v,%v), want ('',false,nil)", v, ok, err)
	}
	if v, err := st.GetSettingDefault("nsfw_display", "blur"); err != nil || v != "blur" {
		t.Fatalf("GetSettingDefault unset = (%q,%v), want ('blur',nil)", v, err)
	}

	if err := st.SetSetting("nsfw_display", "show"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.GetSetting("nsfw_display"); err != nil || !ok || v != "show" {
		t.Fatalf("GetSetting = (%q,%v,%v), want ('show',true,nil)", v, ok, err)
	}
	// Upsert overwrites.
	if err := st.SetSetting("nsfw_display", "hide"); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.GetSettingDefault("nsfw_display", "blur"); v != "hide" {
		t.Fatalf("after overwrite = %q, want hide", v)
	}
}

func TestScanDirsRoundTrip(t *testing.T) {
	st := newTestStore(t)

	if dirs, err := st.ListScanDirs(); err != nil || len(dirs) != 0 {
		t.Fatalf("initial ListScanDirs = (%v,%v), want empty", dirs, err)
	}

	if err := st.AddScanDir("/data/loras"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddScanDir("/data/checkpoints"); err != nil {
		t.Fatal(err)
	}
	// Idempotent add.
	if err := st.AddScanDir("/data/loras"); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListScanDirs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/data/checkpoints", "/data/loras"} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListScanDirs = %v, want %v", got, want)
	}

	if err := st.RemoveScanDir("/data/loras"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListScanDirs()
	if !reflect.DeepEqual(got, []string{"/data/checkpoints"}) {
		t.Fatalf("after remove = %v", got)
	}

	// SetScanDirs replaces the whole set, de-duplicating.
	if err := st.SetScanDirs([]string{"/a", "/b", "/a", ""}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListScanDirs()
	if !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Fatalf("after SetScanDirs = %v, want [/a /b]", got)
	}

	// Empty set clears.
	if err := st.SetScanDirs(nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.ListScanDirs(); len(got) != 0 {
		t.Fatalf("after clear = %v, want empty", got)
	}
}

// TestMigration0006AppliesOnPopulatedDB proves the new migration applies cleanly
// on a DB that already carries data from earlier migrations (a realistic upgrade).
func TestMigration0006AppliesOnPopulatedDB(t *testing.T) {
	st := newTestStore(t)

	// Populate an earlier-migration table so the DB is non-empty.
	mid := 5
	if _, err := st.CreateSubscription(Subscription{
		Kind: KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600,
	}); err != nil {
		t.Fatal(err)
	}

	v, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 7 {
		t.Fatalf("schema version = %d, want 7", v)
	}
	// The new tables are usable on the populated DB.
	if err := st.SetSetting("k", "v"); err != nil {
		t.Fatalf("settings unusable after migration: %v", err)
	}
	if err := st.AddScanDir("/x"); err != nil {
		t.Fatalf("scan_dirs unusable after migration: %v", err)
	}
}
