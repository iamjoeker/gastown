package daemon

import (
	"strings"
	"testing"
)

func TestCompactorCheckIntegrity(t *testing.T) {
	t.Run("empty post-counts is inconclusive, not confirmed loss", func(t *testing.T) {
		pre := map[string]int{"issues": 10, "mail": 3}
		post := map[string]int{}
		_, err := compactorCheckIntegrity(pre, post)
		if err == nil {
			t.Fatal("expected error for empty post-counts, got nil")
		}
		if !strings.Contains(err.Error(), "inconclusive") {
			t.Fatalf("expected an inconclusive-query error, got: %v", err)
		}
		if strings.Contains(err.Error(), "missing after") {
			t.Fatalf("empty result must not be reported as a missing-table integrity failure: %v", err)
		}
	})

	t.Run("both empty is fine (nothing to verify)", func(t *testing.T) {
		gained, err := compactorCheckIntegrity(map[string]int{}, map[string]int{})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if len(gained) != 0 {
			t.Fatalf("expected no gains, got: %v", gained)
		}
	})

	t.Run("genuinely missing table is still caught", func(t *testing.T) {
		pre := map[string]int{"issues": 10, "mail": 3}
		post := map[string]int{"issues": 10}
		_, err := compactorCheckIntegrity(pre, post)
		if err == nil {
			t.Fatal("expected error for missing table, got nil")
		}
		if !strings.Contains(err.Error(), `"mail"`) || !strings.Contains(err.Error(), "missing after") {
			t.Fatalf("expected a missing-table error naming mail, got: %v", err)
		}
	})

	t.Run("lost rows is still caught", func(t *testing.T) {
		pre := map[string]int{"issues": 10}
		post := map[string]int{"issues": 5}
		_, err := compactorCheckIntegrity(pre, post)
		if err == nil {
			t.Fatal("expected error for lost rows, got nil")
		}
		if !strings.Contains(err.Error(), "lost rows") {
			t.Fatalf("expected a lost-rows error, got: %v", err)
		}
	})

	t.Run("gained rows are reported, not an error", func(t *testing.T) {
		pre := map[string]int{"issues": 10}
		post := map[string]int{"issues": 12}
		gained, err := compactorCheckIntegrity(pre, post)
		if err != nil {
			t.Fatalf("expected no error for gained rows, got: %v", err)
		}
		if gained["issues"] != 2 {
			t.Fatalf("expected gained[issues]=2, got: %v", gained)
		}
	})
}
