package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// This file is the guardrail for gt-9tpw: gt emitting a success line, or exit 0,
// from the fact that a command RAN rather than from confirmation that the
// intended effect LANDED.
//
// Seven instances were verified by the Mayor on 2026-08-24. Patching them one at
// a time is explicitly not the job — four of the seven had their own beads and
// the class kept producing new ones. What the instances share is narrower and
// more mechanical than "reports success without checking":
//
//	A function whose entire purpose is to make something happen somewhere else
//	discovers that it did not happen, writes that discovery to a log, and
//	RETURNS NOTHING — so no caller can make its success line conditional on it.
//
// Three verified cases have exactly that shape, and none of them could have been
// caught by reading the caller:
//
//   - gt-32gf  watchAndDeliver logged the tmux send-keys failure and returned
//     nothing. runNudge printed "✓ Nudged" one line below the error, exit 0.
//   - gt-lae6  nudgeWitness returns nothing, so gt done's durable checkpoint
//     hardcodes "ok" — written BEFORE the attempt it claims to describe.
//   - gt-9tpw  Daemon.escalate returned nothing, so a destructive reaper
//     fallback reported itself escalated when the escalation had failed.
//
// In each, the caller is blameless: it had nothing to test. The defect is in the
// signature. That is what makes this checkable at all, and it is why the rule
// below is about return types rather than about print statements — a rule about
// print statements cannot see the three cases above, because the print is
// correct code sitting downstream of a function that lied by omission.

// deliveryVerbs name functions that exist to make something happen elsewhere —
// another process, another agent, another machine.
//
// The list is deliberately not "every mutating verb". For a function that
// computes or formats, a void return is ordinary and correct. For one named
// "notify", "send" or "escalate", whether it arrived is the ONLY thing the
// caller wants to know, and discarding it is never incidental.
var deliveryVerbs = []string{
	"send", "deliver", "notify", "nudge", "escalate",
	"announce", "broadcast", "dispatch", "publish", "submit",
}

// deliveryReportPackages are the packages this rule is enforced in: the ones
// that deliver to agents and to the town. Adding a package here is welcome;
// removing one needs a reason better than "it started failing".
var deliveryReportPackages = []string{
	"internal/acp",
	"internal/cmd",
	"internal/daemon",
	"internal/deacon",
	"internal/mail",
	"internal/nudge",
	"internal/refinery",
	"internal/witness",
}

// knownVoidDeliveryReporters is the baseline: delivery reporters that swallow a
// failure and cannot tell their caller, as of gt-9tpw.
//
// It is a RATCHET, not an allowlist. The test fails when an entry appears that
// is not here, and it also fails when an entry here no longer violates — so
// fixing one requires deleting its line, and the list can only shrink. An
// allowlist that could be appended to without noticing would reproduce the
// problem it exists to stop.
//
// Entries are "<package path>.<function>". No line numbers: they churn on every
// edit above them and would make this list a merge conflict generator.
//
// Being on this list is not approval. It records that the function was in this
// state when the rule was introduced and that converting it was out of scope for
// one bead — converting a reporter to return an error means auditing every
// caller to decide what they should now do, which is the work each of these
// deserves and none of them got.
var knownVoidDeliveryReporters = []string{
	"internal/acp.deliverNudges",
	"internal/acp.notifyWithMeta",
	"internal/cmd.notifyConvoyCompletion",
	"internal/cmd.notifyDoneCloseSkipped",
	"internal/cmd.notifyDoneMRRefused",
	"internal/cmd.notifyDoneSkipVerifyUsed",
	"internal/cmd.notifyMayorSession",
	"internal/cmd.nudgeFormulaDog",
	"internal/cmd.nudgeRefinery",
	"internal/cmd.nudgeWitness",
	"internal/cmd.sendCloseNotification",
	"internal/cmd.sendMail",
	"internal/cmd.sendZombieNotification",
	"internal/daemon.dispatchPlugins",
	"internal/daemon.dispatchQueuedWork",
	"internal/daemon.escalate",
	"internal/daemon.notifySlack",
	"internal/daemon.notifyWitnessOfCrashedPolecat",
	"internal/daemon.notifyWitnessOfGUPP",
	"internal/daemon.notifyWitnessOfOrphanedWork",
	"internal/refinery.notifyConvoyCompletion",
	"internal/refinery.notifyDeaconConvoyFeeding",
	"internal/refinery.notifyWorkerRejected",

	// The two below were found BY this rule rather than by the gt-9tpw sweep, and
	// are the sharpest instances in the list: notifyMayorSchedulerOpen tries a
	// channel event, then a tmux nudge, then `gt mail send`, and discards the
	// failure of all three. Its whole purpose is telling the Mayor there is free
	// scheduler capacity, so a silent total failure stalls dispatch with nothing
	// anywhere saying so. Deferred, not dismissed: notifyMayorSlotOpen has 11 call
	// sites and deciding what each should do with a delivery failure is the work
	// these deserve, which is more than this bead can carry. Filed as gt-sm80.
	"internal/witness.notifyMayorSchedulerOpen",
	"internal/witness.notifyMayorSlotOpen",
}

// TestDeliveryReportersReturnTheirFailure is the gt-9tpw class guardrail.
//
// A NEW delivery reporter that swallows its failure fails this test. The fix is
// to return the error — not to add a line to the baseline.
func TestDeliveryReportersReturnTheirFailure(t *testing.T) {
	repoRoot := repoRootForPolicy(t)

	found := map[string]string{} // key -> file:line, for the failure message
	for _, pkg := range deliveryReportPackages {
		dir := filepath.Join(repoRoot, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			for key, where := range voidDeliveryReporters(t, pkg, path) {
				found[key] = where
			}
		}
	}

	// Control: the detector must still recognise the shape it was built from. If
	// this fires, the rule has gone blind and every "no new violations" pass below
	// it is meaningless — a clean result from a detector that cannot detect is the
	// exact failure gt-9tpw is about, committed in the guardrail itself.
	const control = "internal/cmd.nudgeWitness"
	if _, ok := found[control]; !ok {
		t.Fatalf("known-positive control %q not detected: the rule no longer recognises "+
			"a void delivery reporter, so this test proves nothing. Fix the detector before "+
			"trusting any result from it. (gt-lae6 is the bead that documents this function.)", control)
	}

	baseline := map[string]bool{}
	for _, k := range knownVoidDeliveryReporters {
		baseline[k] = true
	}

	var added, fixed []string
	for key, where := range found {
		if !baseline[key] {
			added = append(added, fmt.Sprintf("  %s\t(%s)", key, where))
		}
	}
	for key := range baseline {
		if _, ok := found[key]; !ok {
			fixed = append(fixed, "  "+key)
		}
	}
	sort.Strings(added)
	sort.Strings(fixed)

	if len(added) > 0 {
		t.Errorf(`new delivery reporter(s) that swallow a failure and return nothing:

%s
A function named for delivery must tell its caller whether delivery happened.
Returning nothing means no caller can condition a success line on it, which is
how "✓ Nudged" printed one line under a tmux failure with exit 0 (gt-32gf), and
how gt done's checkpoint recorded "ok" for a witness nudge it never confirmed
(gt-lae6).

Return the error. Do NOT add a line to knownVoidDeliveryReporters — that list
only shrinks.`, strings.Join(added, "\n"))
	}

	if len(fixed) > 0 {
		t.Errorf(`knownVoidDeliveryReporters lists function(s) that no longer violate:

%s
Delete those lines. The baseline is a ratchet; a stale entry silently re-permits
the defect if the function ever regresses.`, strings.Join(fixed, "\n"))
	}
}

func repoRootForPolicy(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// voidDeliveryReporters returns the violating functions in one file, keyed
// "<pkg>.<func>" and valued "<file>:<line>".
func voidDeliveryReporters(t *testing.T, pkg, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	out := map[string]string{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if !hasDeliveryVerb(fd.Name.Name) || reportsItsOutcome(fd) {
			continue
		}
		if !swallowsAFailure(fd.Body) {
			continue
		}
		pos := fset.Position(fd.Pos())
		out[pkg+"."+fd.Name.Name] = fmt.Sprintf("%s:%d", filepath.Base(path), pos.Line)
	}
	return out
}

func hasDeliveryVerb(name string) bool {
	lower := strings.ToLower(name)
	for _, v := range deliveryVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	return false
}

// reportsItsOutcome reports whether the signature gives the caller ANY way to
// learn what happened.
//
// Three shapes count, and the rule needs all three or it flags correct code:
//
//   - an error (or bool) result — the ordinary way;
//   - a status STRUCT result, since gt-3i4e's fix to `gt escalate` reports
//     through one, and a rule that rejected it would push authors back to void;
//   - a POINTER PARAMETER to such a struct, written to as an out-parameter.
//
// The third was found by this rule flagging witness.notifyRefineryMergeReady,
// which looks void and is not: it records the nudge failure into the
// *HandlerResult it is handed. Its two neighbours in the same file, flagged in
// the same run, turned out to be real — they take no result and discard
// channelevents errors into `_, _ =`. A rule that cannot tell those apart is
// worth little, since the false positive sits three functions from the true one.
//
// For the out-parameter shape the pointer is not enough: the body must ASSIGN
// to a field of it. Accepting the parameter alone let sendZombieNotification
// through, which takes a *DetectZombiePolecatsResult it only READS — the
// detection it is reporting on, not a place to record whether the mail landed.
// "Has a Result-shaped pointer" and "tells the caller what happened" are not
// the same sentence, and the first is what a signature can show you.
func reportsItsOutcome(fd *ast.FuncDecl) bool {
	ft := fd.Type
	if ft.Results != nil {
		for _, r := range ft.Results.List {
			if typeReportsOutcome(r.Type) {
				return true
			}
		}
	}
	if ft.Params == nil {
		return false
	}
	for _, p := range ft.Params.List {
		star, ok := p.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if named := typeIdentName(star.X); named == "" || !outcomeTypeName(named) {
			continue
		}
		for _, n := range p.Names {
			if assignsToFieldOf(fd.Body, n.Name) {
				return true
			}
		}
	}
	return false
}

// assignsToFieldOf reports whether the body writes to <name>.<Field>.
func assignsToFieldOf(body *ast.BlockStmt, name string) bool {
	if name == "" || name == "_" {
		return false
	}
	written := false
	ast.Inspect(body, func(n ast.Node) bool {
		if written {
			return false
		}
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
				written = true
				return false
			}
		}
		return true
	})
	return written
}

func typeIdentName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func typeReportsOutcome(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		if t.Name == "error" || t.Name == "bool" {
			return true
		}
		return outcomeTypeName(t.Name)
	case *ast.StarExpr:
		return typeReportsOutcome(t.X)
	case *ast.SelectorExpr:
		return outcomeTypeName(t.Sel.Name)
	case *ast.ArrayType:
		return typeReportsOutcome(t.Elt)
	}
	return false
}

func outcomeTypeName(name string) bool {
	for _, suffix := range []string{"Result", "Status", "Outcome", "Report", "Receipt"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// swallowsAFailure reports whether the body discovers a failure and keeps it:
// either by handing an error-shaped value to a logger, or by discarding the
// result of a call into _.
func swallowsAFailure(body *ast.BlockStmt) bool {
	swallowed := false
	ast.Inspect(body, func(n ast.Node) bool {
		if swallowed {
			return false
		}
		switch t := n.(type) {
		case *ast.CallExpr:
			name := calleeNameForPolicy(t.Fun)
			if !isLoggingCall(name) {
				return true
			}
			for _, a := range t.Args {
				if mentionsAnError(a) {
					swallowed = true
					return false
				}
			}
		case *ast.AssignStmt:
			if len(t.Rhs) != 1 {
				return true
			}
			call, ok := t.Rhs[0].(*ast.CallExpr)
			if !ok || !discardsEveryResult(t.Lhs) {
				return true
			}
			if isPureHelper(calleeNameForPolicy(call.Fun)) {
				return true
			}
			swallowed = true
			return false
		}
		return true
	})
	return swallowed
}

func discardsEveryResult(lhs []ast.Expr) bool {
	for _, l := range lhs {
		id, ok := l.(*ast.Ident)
		if !ok || id.Name != "_" {
			return false
		}
	}
	return len(lhs) > 0
}

// isPureHelper excludes calls whose discarded result is not a failure at all.
// strings.Cut returns a bool, not an error; counting it made the first version
// of this rule report violations in functions that never touched IO.
func isPureHelper(name string) bool {
	for _, prefix := range []string{"strings.", "bytes.", "sort.", "math.", "fmt.Sprint", "utf8."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isLoggingCall matches the writers a swallowed failure gets written to.
//
// "otellog." is excluded on purpose: it is a structured-attribute constructor,
// not a logger call, and matching it on the substring "log." classified pure
// field builders as failure swallowing.
func isLoggingCall(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "otellog.") {
		return false
	}
	switch {
	case strings.Contains(lower, "printwarning"),
		strings.Contains(lower, "logger."),
		strings.HasPrefix(lower, "log."),
		strings.HasSuffix(lower, ".printf"),
		strings.HasSuffix(lower, ".println"),
		strings.HasSuffix(lower, ".logf"),
		strings.HasPrefix(lower, "fmt.fprint"):
		return true
	}
	return false
}

func mentionsAnError(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		lower := strings.ToLower(id.Name)
		if lower == "err" || strings.HasSuffix(lower, "err") || strings.HasSuffix(lower, "error") {
			found = true
			return false
		}
		return true
	})
	return found
}

func calleeNameForPolicy(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return calleeNameForPolicy(t.X) + "." + t.Sel.Name
	}
	return ""
}
