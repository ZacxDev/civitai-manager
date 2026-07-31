package store

import "testing"

// rewind0018 puts an already-migrated test store back into its PRE-0018 state:
// the schema_migrations bookkeeping row is removed (so migrate() re-runs 0018)
// and the settings table is reset to whatever the old install held.
//
// It deliberately drives the REAL migrate() rather than re-executing the .sql by
// hand — a test that pastes the SQL cannot catch a migration that never runs.
func rewind0018(t *testing.T, st *Store, oldNSFWDisplay string) {
	t.Helper()
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM settings WHERE key IN ('nsfw_display','maturity_range')`); err != nil {
		t.Fatal(err)
	}
	if oldNSFWDisplay != "" {
		if err := st.SetSetting("nsfw_display", oldNSFWDisplay); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigration0018MapsEveryStoredNSFWModeToTheFullRange is the upgrade guard.
//
// blur, show AND hide all become "pg:xxx": nothing the user could already see
// may disappear on upgrade. hide is included even though it was normalized away
// in code — a row could still be sitting in an old DB.
func TestMigration0018MapsEveryStoredNSFWModeToTheFullRange(t *testing.T) {
	for _, old := range []string{"blur", "show", "hide"} {
		t.Run(old, func(t *testing.T) {
			st := newTestStore(t)
			rewind0018(t, st, old)

			if err := st.migrate(); err != nil {
				t.Fatalf("migrate: %v", err)
			}

			v, ok, err := st.GetSetting("maturity_range")
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("stored nsfw_display=%q did not produce a maturity_range row", old)
			}
			if v != "pg:xxx" {
				t.Errorf("stored nsfw_display=%q migrated to %q, want the FULL range \"pg:xxx\"", old, v)
			}
			// The dead key must not linger.
			if _, still, _ := st.GetSetting("nsfw_display"); still {
				t.Errorf("nsfw_display row survived the migration")
			}
		})
	}
}

// TestMigration0018LeavesAFreshInstallSettingLess: with no old row there is
// nothing to migrate, so no row is written and the CODE default (also the full
// range) governs. Writing one here would freeze the default in every DB.
func TestMigration0018LeavesAFreshInstallSettingLess(t *testing.T) {
	st := newTestStore(t)
	rewind0018(t, st, "")

	if err := st.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if v, ok, _ := st.GetSetting("maturity_range"); ok {
		t.Errorf("fresh install got a stored maturity_range = %q, want none", v)
	}
}

// TestMigration0018IsIdempotent — migrate() must survive a re-run (it is also
// how rewind0018 works), and must not clobber a range the user has since set.
func TestMigration0018IsIdempotent(t *testing.T) {
	st := newTestStore(t)
	rewind0018(t, st, "blur")
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	// The user narrows the band.
	if err := st.SetSetting("maturity_range", "pg:r"); err != nil {
		t.Fatal(err)
	}
	// A second upgrade attempt (e.g. a downgrade/upgrade cycle) must not reset it.
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version >= 18`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("nsfw_display", "show"); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.GetSetting("maturity_range"); v != "pg:r" {
		t.Errorf("re-run clobbered the user's range: %q, want \"pg:r\"", v)
	}
}

// TestMigration0018InvalidatesTheCommunityCache: every cached body predates the
// over-fetch, so a narrow band would render short on exactly the model versions
// the user has already visited.
func TestMigration0018InvalidatesTheCommunityCache(t *testing.T) {
	st := newTestStore(t)
	if err := st.PutCommunityCache(101, 202, "Mature", []byte(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetCommunityCache(101, 202, "Mature"); e == nil {
		t.Fatal("seed did not land")
	}

	rewind0018(t, st, "blur")
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}

	if e, _ := st.GetCommunityCache(101, 202, "Mature"); e != nil {
		t.Errorf("pre-0018 community_cache row survived the migration")
	}
}
