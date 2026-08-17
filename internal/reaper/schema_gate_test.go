package reaper

import (
	"reflect"
	"sort"
	"testing"
)

// HasReaperSchema decides whether the reaper touches a database at all, so an
// answer that is wrong in either direction is expensive: a false "yes" runs
// deletes against a half-migrated database, and a false "no" silently skips a
// live one. Its answer depends on which tables and columns exist, which no
// source scan can observe — these cases damage a real schema and read the gate.
func TestHasReaperSchemaBehaviour(t *testing.T) {
	cases := []struct {
		name    string
		dbName  string
		damage  []string
		want    bool
		because string
	}{
		{
			name:    "complete schema",
			dbName:  "gate_complete",
			want:    true,
			because: "the fixture carries every table and column the reaper reads",
		},
		{
			name:    "wisps table missing",
			dbName:  "gate_no_wisps",
			damage:  []string{"DROP TABLE wisps"},
			want:    false,
			because: "the reaper has no wisps to reap or purge",
		},
		{
			name:   "wisp_dependencies missing a typed target column",
			dbName: "gate_untyped_wisp_deps",
			damage: []string{"ALTER TABLE wisp_dependencies DROP COLUMN depends_on_external"},
			want:   false,
			because: "the parent-exclusion join reads depends_on_external; without it the reaper " +
				"would treat wisps with external parents as orphans",
		},
		{
			name:   "dependencies table absent entirely",
			dbName: "gate_no_dependencies",
			damage: []string{"DROP TABLE dependencies"},
			want:   true,
			because: "issue dependencies live on a separate Dolt instance in some towns; the wisp " +
				"side is still safe to sweep",
		},
		{
			name:   "dependencies present but missing a typed target column",
			dbName: "gate_untyped_deps",
			damage: []string{"ALTER TABLE dependencies DROP COLUMN depends_on_wisp_id"},
			want:   false,
			because: "a half-migrated dependencies table is worse than an absent one — AutoClose " +
				"would read blockers from a schema it does not understand",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.dbName)
			for _, stmt := range tc.damage {
				f.exec(t, stmt)
			}
			got, err := HasReaperSchema(f.db)
			if err != nil {
				t.Fatalf("HasReaperSchema: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasReaperSchema = %v, want %v — %s", got, tc.want, tc.because)
			}
		})
	}
}

// TestDiscoverDatabasesBehaviour covers the test-pollution filter. Orphan
// testdb_/beads_t/doctest_ databases accumulate on the production server; if
// discovery returned them the reaper would sweep databases nobody owns.
func TestDiscoverDatabasesBehaviour(t *testing.T) {
	f := newFixture(t, "discover_prod")
	f.createDatabase(t, "hq")
	for _, polluted := range []string{"testdb_abc", "beads_t1", "beads_pt2", "doctest_x"} {
		f.createDatabase(t, polluted)
	}

	got := DiscoverDatabases(f.host, f.port)
	sort.Strings(got)
	want := []string{"discover_prod", "hq"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverDatabases = %v, want %v — system and test-pollution databases must be filtered", got, want)
	}
}

// TestDiscoverDatabasesFallsBackWhenServerUnreachable pins the failure mode:
// discovery must not return an empty list, which callers would read as "no
// databases to sweep" rather than "the server is down".
func TestDiscoverDatabasesFallsBackWhenServerUnreachable(t *testing.T) {
	// Port 1 is reserved and never accepts connections.
	got := DiscoverDatabases("127.0.0.1", 1)
	if !reflect.DeepEqual(got, DefaultDatabases) {
		t.Errorf("DiscoverDatabases on an unreachable server = %v, want the %v fallback", got, DefaultDatabases)
	}
}

// TestIsTableNotFound guards the error classification that lets the reaper skip
// databases whose issues/labels tables live on a different Dolt instance. A
// mistake here converts a benign skip into a hard failure of the whole sweep.
func TestIsTableNotFound(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"table not found: issues", true},
		{"Error 1146 (42S02): table not found: labels", true},
		{"table 'hq.labels' doesn't exist", true},
		{"TABLE NOT FOUND", true},
		{"connection refused", false},
		{"nothing to commit", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = errorString(c.msg)
		}
		if got := isTableNotFound(err); got != c.want {
			t.Errorf("isTableNotFound(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
