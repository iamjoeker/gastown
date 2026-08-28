package style

import (
	"strings"
	"testing"
)

// TestTableRender_CutCellsShowAnEllipsis pins the contract the table already
// keeps and that gt-2izk depended on: a cell that does not fit is rendered with
// a visible "...". Callers must not pre-shorten values to the column width —
// doing so lands on `len == Width`, one short of the `len > Width` rule here, so
// the cut happens and nothing marks it.
func TestTableRender_CutCellsShowAnEllipsis(t *testing.T) {
	tests := []struct {
		name  string
		width int
		value string
		want  string
	}{
		{name: "fits exactly, untouched", width: 12, value: "gt-wisp-2hfh", want: "gt-wisp-2hfh"},
		// The bead's decisive case: gt-wisp-2hfhc in a 12-wide column. The old
		// caller pre-cut it to "gt-wisp-2hfh", which is a valid-looking id that
		// resolves to nothing. Here it renders visibly cut.
		{name: "one over, cut with ellipsis", width: 12, value: "gt-wisp-2hfhc", want: "gt-wisp-2..."},
		{name: "far over, cut with ellipsis", width: 8, value: "polecat/chrome/gt-wkcz", want: "polec..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := NewTable(Column{Name: "ID", Width: tt.width}).AddRow(tt.value).Render()
			if !strings.Contains(out, tt.want) {
				t.Fatalf("rendered %q, want it to contain %q", out, tt.want)
			}
			// The defining property: if anything was dropped, the reader can see
			// that something was dropped.
			if len(tt.value) > tt.width && !strings.Contains(out, "...") {
				t.Fatalf("value %q was cut to width %d without an ellipsis: %q", tt.value, tt.width, out)
			}
		})
	}
}

// TestTableRender_NarrowColumnDoesNotPanic covers the widths that cannot fit an
// ellipsis at all. Slicing to Width-3 there is a negative index.
func TestTableRender_NarrowColumnDoesNotPanic(t *testing.T) {
	for _, width := range []int{1, 2, 3, 4} {
		out := NewTable(Column{Name: "X", Width: width}).AddRow("abcdefgh").Render()
		if out == "" {
			t.Fatalf("width %d rendered nothing", width)
		}
	}
}
