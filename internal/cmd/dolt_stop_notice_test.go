package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// The stop notice is the correction for gt-09e4: `gt dolt stop` used to print
// "Dolt server stopped" whether or not the server stayed stopped. Under a
// supervisor it does not, and an operator who believes the success line retries
// the command instead of reaching for the one that works.

func TestSupervisedStopNoticeContradictsTheStop(t *testing.T) {
	sup := &doltserver.Supervisor{
		Kind:     "systemd",
		Unit:     "gt-dolt.service",
		UserUnit: true,
		Restart:  "always",
	}
	out := supervisedStopNotice(3580495, sup)

	// The notice's whole job is to deny the stop. A reader who skims must not be
	// able to come away thinking the server is down.
	if !strings.Contains(out, "NOT stopped") {
		t.Errorf("notice must say the server is not stopped, got:\n%s", out)
	}
	if strings.Contains(out, "Dolt server stopped") {
		t.Errorf("notice must not repeat the old success claim, got:\n%s", out)
	}

	// It must name the unit and hand over the command that actually stops it —
	// a warning without the remedy is what leaves operators retrying.
	if !strings.Contains(out, "gt-dolt.service") {
		t.Errorf("notice must name the supervising unit, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("notice must give the working stop command, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user start gt-dolt.service") {
		t.Errorf("notice must give the way back up, got:\n%s", out)
	}

	// Stopping the unit takes the whole town's data plane down; the notice must
	// not present it as a routine next step.
	if !strings.Contains(out, "offline for") {
		t.Errorf("notice must state the blast radius, got:\n%s", out)
	}

	// Retrying is the specific wrong move this exists to prevent.
	if !strings.Contains(out, "again") {
		t.Errorf("notice must tell the operator not to retry, got:\n%s", out)
	}

	// The PID belongs in the text: a changed PID on the next check is the thing
	// that reads as "something is wrong" without this explanation.
	if !strings.Contains(out, "3580495") {
		t.Errorf("notice must name the PID it signalled, got:\n%s", out)
	}
}

func TestSupervisedStopNoticeForSystemUnit(t *testing.T) {
	sup := &doltserver.Supervisor{
		Kind:     "systemd",
		Unit:     "dolt.service",
		UserUnit: false,
		Restart:  "always",
	}
	out := supervisedStopNotice(4242, sup)

	if !strings.Contains(out, "systemctl stop dolt.service") {
		t.Errorf("system-scope unit needs a system-scope stop command, got:\n%s", out)
	}
	if strings.Contains(out, "--user") {
		t.Errorf("system-scope unit must not be given a --user command, got:\n%s", out)
	}
}
