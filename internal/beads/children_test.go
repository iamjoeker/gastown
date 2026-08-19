package beads

import "testing"

// TestParseChildrenJSONParentKeyedEnvelope pins the shape bd actually returns,
// captured from a live `bd show hq-wisp-wr3tpm --children --json` on 2026-08-19:
// an object keyed by the PARENT ID, with a schema_version sibling. The two
// shapes a reader would guess — a bare array, or {"children": [...]} — both
// decode without error and yield nothing, so guessing wrong produces a
// confident "0 children" for a molecule that has six.
func TestParseChildrenJSONParentKeyedEnvelope(t *testing.T) {
	raw := `{
	  "hq-wisp-wr3tpm": [
	    {"id": "hq-wisp-2plhd5", "title": "Scan databases for reaper candidates", "status": "open"},
	    {"id": "hq-wisp-npn0rw", "title": "Reap stale wisps", "status": "open"}
	  ],
	  "schema_version": 1
	}`

	children, err := ParseChildrenJSON(raw)
	if err != nil {
		t.Fatalf("ParseChildrenJSON: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2: %+v", len(children), children)
	}
	if children[0].ID != "hq-wisp-2plhd5" || children[1].ID != "hq-wisp-npn0rw" {
		t.Errorf("unexpected child IDs: %+v", children)
	}
	if children[0].Title != "Scan databases for reaper candidates" {
		t.Errorf("title not decoded: %+v", children[0])
	}
}

func TestParseChildrenJSONBareArray(t *testing.T) {
	children, err := ParseChildrenJSON(`[{"id": "hq-wisp-a", "status": "closed"}]`)
	if err != nil {
		t.Fatalf("ParseChildrenJSON: %v", err)
	}
	if len(children) != 1 || children[0].ID != "hq-wisp-a" {
		t.Errorf("got %+v", children)
	}
}

func TestParseChildrenJSONRejectsUnusableInput(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"whitespace":          "   \n",
		"not json":            "no children found",
		"no child arrays":     `{"schema_version": 1}`,
		"non-array payload":   `{"hq-wisp-a": {"id": "x"}}`,
		"malformed array":     `{"hq-wisp-a": [`,
		"empty child payload": `{"hq-wisp-a": }`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// An error is the point: these must not come back as an empty slice
			// with a nil error, which reads as "this molecule has no steps".
			if children, err := ParseChildrenJSON(raw); err == nil {
				t.Errorf("expected an error, got %d children", len(children))
			}
		})
	}
}
