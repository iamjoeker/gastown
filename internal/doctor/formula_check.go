package doctor

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/formula"
)

// FormulaCheck verifies that embedded formulas are up-to-date.
// It detects outdated formulas (binary updated), missing formulas (user deleted),
// and modified formulas (user customized). Can auto-fix outdated and missing.
type FormulaCheck struct {
	FixableCheck

	// lastFix records what the most recent Fix() actually did. The doctor
	// framework re-runs Run() after a successful Fix() and shows only that
	// second result, so this is the only channel a fix has for reporting the
	// files it deliberately declined to touch. Throwing these counts away is
	// what made a correct refusal indistinguishable from a completed update
	// (gt-bxu). Checks run sequentially, so a plain field is enough.
	lastFix *formula.UpdateReport
}

// NewFormulaCheck creates a new formula check.
func NewFormulaCheck() *FormulaCheck {
	return &FormulaCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "formulas",
				CheckDescription: "Check embedded formulas are up-to-date",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks if formulas need updating.
func (c *FormulaCheck) Run(ctx *CheckContext) *CheckResult {
	report, err := formula.CheckFormulaHealth(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not check formulas: %v", err),
		}
	}

	// All good
	if report.Outdated == 0 && report.Missing == 0 && report.Modified == 0 && report.New == 0 && report.Untracked == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("%d formulas up-to-date", report.OK),
		}
	}

	// Build details
	var details []string
	var needsFix bool

	for _, f := range report.Formulas {
		switch f.Status {
		case "outdated":
			details = append(details, fmt.Sprintf("  %s: update available", f.Name))
			needsFix = true
		case "missing":
			details = append(details, fmt.Sprintf("  %s: missing (will reinstall)", f.Name))
			needsFix = true
		case "modified":
			if f.EmbeddedChanged {
				name := strings.TrimSuffix(f.Name, ".formula.toml")
				details = append(details, fmt.Sprintf("  %s: locally modified AND the shipped default has changed since install — reconcile by hand (gt will keep skipping it): gt formula drift %s", f.Name, name))
			} else {
				details = append(details, fmt.Sprintf("  %s: locally modified (skipping)", f.Name))
			}
		case "new":
			details = append(details, fmt.Sprintf("  %s: new formula available", f.Name))
			needsFix = true
		case "untracked":
			details = append(details, fmt.Sprintf("  %s: untracked (will update)", f.Name))
			needsFix = true
		}
	}

	// If a --fix just ran, say what it declined to do. Without this the only
	// visible outcome of the fix is the unchanged warning it could not clear.
	details = append(details, c.fixDetails()...)

	// Determine status. Drifted local modifications are not auto-fixable —
	// overwriting would discard the customization — but they must still warn:
	// a formula fix shipped in the binary never reaches a town that customized
	// that formula, and reporting OK is how such a fix goes unnoticed (gt-0sq).
	status := StatusOK
	if needsFix || report.ModifiedDrift > 0 {
		status = StatusWarning
	}

	// Build message
	var parts []string
	if report.Outdated > 0 {
		parts = append(parts, fmt.Sprintf("%d outdated", report.Outdated))
	}
	if report.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", report.Missing))
	}
	if report.New > 0 {
		parts = append(parts, fmt.Sprintf("%d new", report.New))
	}
	if report.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", report.Untracked))
	}
	if report.Modified > 0 {
		if report.ModifiedDrift > 0 {
			parts = append(parts, fmt.Sprintf("%d modified (%d shadowing a newer shipped default)", report.Modified, report.ModifiedDrift))
		} else {
			parts = append(parts, fmt.Sprintf("%d modified", report.Modified))
		}
	}

	message := fmt.Sprintf("Formulas: %s", strings.Join(parts, ", "))

	result := &CheckResult{
		Name:    c.Name(),
		Status:  status,
		Message: message,
		Details: details,
	}

	// A drifted formula must never be sent to the generic update hint on its
	// own account: --fix provably cannot move it, so the operator would run the
	// fix and meet the identical warning. When both kinds are present the hint
	// has to carry both halves, or the drift disappears behind the fixable part.
	switch {
	case needsFix && report.ModifiedDrift > 0:
		result.FixHint = "Run 'gt doctor --fix' for the updatable formulas; the drifted ones above stay put — diff them against the shipped defaults and merge by hand"
	case needsFix:
		result.FixHint = "Run 'gt doctor --fix' to update formulas"
	case report.ModifiedDrift > 0:
		result.FixHint = "Run 'gt formula drift' to reconcile — --fix cannot touch locally modified formulas"
	}

	return result
}

// fixDetails renders what the most recent Fix() refused to do, for the re-run
// that follows it. Empty before any fix, or when the fix touched everything.
func (c *FormulaCheck) fixDetails() []string {
	if c.lastFix == nil || len(c.lastFix.Skipped) == 0 {
		return nil
	}

	var out []string
	out = append(out, fmt.Sprintf("  --fix left %d locally modified formula(s) untouched: %s",
		len(c.lastFix.Skipped), strings.Join(c.lastFix.Skipped, ", ")))
	if len(c.lastFix.Drifted) > 0 {
		out = append(out, fmt.Sprintf("  of those, %s also have a newer shipped default — re-running --fix will never update them",
			strings.Join(c.lastFix.Drifted, ", ")))
	}
	return out
}

// Fix updates outdated and missing formulas.
//
// Formulas the user has customized are skipped, which is correct — overwriting
// would discard the customization. The skip is recorded rather than discarded so
// the re-run that follows can report it: a refusal nobody hears about reads as a
// fix that worked, and the drift it leaves behind is never mentioned again.
func (c *FormulaCheck) Fix(ctx *CheckContext) error {
	report, err := formula.UpdateFormulasDetailed(ctx.TownRoot)
	c.lastFix = report
	return err
}
