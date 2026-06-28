package main

import (
	"strings"
	"testing"

	"github.com/justinstimatze/gusset/internal/converge"
	"github.com/justinstimatze/gusset/internal/status"
)

// TestOutcomeLogLine pins the activity-log mapping for every reconcile action:
// the four consequential outcomes a tester needs to see are logged at the right
// level, and LocalNewer (the common no-op) stays silent so it can't flood the
// bounded ring. The messages must carry only ids/labels, never data values.
func TestOutcomeLogLine(t *testing.T) {
	const label = "laptop"
	cases := []struct {
		action    converge.Action
		wantLog   bool
		wantLevel status.LogLevel
	}{
		{converge.Applied, true, status.LogOK},
		{converge.Locked, true, status.LogWarn},
		{converge.Blocked, true, status.LogWarn},
		{converge.Failed, true, status.LogError},
		{converge.LocalNewer, false, ""},
	}
	for _, c := range cases {
		o := converge.Outcome{Extension: "uBlock0@raymondhill.net", Action: c.action}
		level, msg, ok := outcomeLogLine(o, label)
		if ok != c.wantLog {
			t.Errorf("%s: ok = %v, want %v", c.action, ok, c.wantLog)
			continue
		}
		if !ok {
			continue
		}
		if level != c.wantLevel {
			t.Errorf("%s: level = %q, want %q", c.action, level, c.wantLevel)
		}
		if !strings.Contains(msg, o.Extension) {
			t.Errorf("%s: message %q omits the extension id", c.action, msg)
		}
		if !strings.Contains(msg, label) {
			t.Errorf("%s: message %q omits the peer label", c.action, msg)
		}
	}
}

// TestOutcomeLogLine_NoDataLeak guards the privacy invariant: a Detail field
// (which can hold internal error text) must never reach the activity log.
func TestOutcomeLogLine_NoDataLeak(t *testing.T) {
	o := converge.Outcome{
		Extension: "uBlock0@raymondhill.net",
		Action:    converge.Failed,
		Detail:    "SECRET-SHOULD-NOT-APPEAR",
	}
	_, msg, ok := outcomeLogLine(o, "laptop")
	if !ok {
		t.Fatal("Failed outcome should be logged")
	}
	if strings.Contains(msg, "SECRET-SHOULD-NOT-APPEAR") {
		t.Errorf("activity-log message leaked the outcome Detail: %q", msg)
	}
}
