package emu

import "testing"

func write(t *testing.T, e Emulator, s string) {
	t.Helper()
	if _, err := e.Write([]byte(s)); err != nil {
		t.Fatalf("write %q: %v", s, err)
	}
}

// TestPromptCountSurvivesAClearScreen pins the counter WaitForPrompt reads.
// The emulator drops the markers on screen when the screen is cleared, and a
// count read off its marker list therefore went down across `clear`: one
// prompt, a clear, one new prompt left the count where it started, and a wait
// for one more prompt than before never ended.
func TestPromptCountSurvivesAClearScreen(t *testing.T) {
	e := New(20, 5)
	write(t, e, "\x1b]133;A\x07$ ")
	if got := e.PromptCount(); got != 1 {
		t.Fatalf("PromptCount after one prompt = %d, want 1", got)
	}
	write(t, e, "\x1b[2J\x1b[H\x1b]133;A\x07$ ")
	if got := e.PromptCount(); got != 2 {
		t.Errorf("PromptCount after clear and a new prompt = %d, want 2", got)
	}
	write(t, e, "\x1b[3J\x1b]133;D;7\x07")
	if got := e.CommandFinishedCount(); got != 1 {
		t.Errorf("CommandFinishedCount = %d, want 1", got)
	}
	write(t, e, "\x1b[3J")
	if code, ok := e.LastCommandExit(); !ok || code != 7 {
		t.Errorf("LastCommandExit after ED 3 = (%d, %v), want (7, true)", code, ok)
	}
}

func TestLastCommandExit(t *testing.T) {
	cases := []struct {
		name  string
		input string
		code  int
		ok    bool
	}{
		{"none yet", "", 0, false},
		{"zero", "\x1b]133;D;0\x07", 0, true},
		{"non-zero", "\x1b]133;D;130\x07", 130, true},
		{"no code", "\x1b]133;D\x07", -1, true},
		{"code then options", "\x1b]133;D;2;aid=12\x07", 2, true},
		{"latest wins", "\x1b]133;D;1\x07\x1b]133;D;0\x07", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New(20, 5)
			write(t, e, tc.input)
			code, ok := e.LastCommandExit()
			if code != tc.code || ok != tc.ok {
				t.Errorf("LastCommandExit = (%d, %v), want (%d, %v)", code, ok, tc.code, tc.ok)
			}
		})
	}
}

// TestModesReportTheOriginalAltScreen covers mode 47, which the emulator acts
// on but did not report, so TermState could not see a program that left it set.
func TestModesReportTheOriginalAltScreen(t *testing.T) {
	e := New(20, 5)
	write(t, e, "\x1b[?47h")
	if !e.Modes()[47] {
		t.Fatalf("Modes() = %v, want 47 set after CSI ?47h", e.Modes())
	}
	write(t, e, "\x1b[?47l")
	if e.Modes()[47] {
		t.Errorf("Modes() = %v, want 47 clear after CSI ?47l", e.Modes())
	}
	write(t, e, "\x1b[?47h\x1bc")
	if e.Modes()[47] {
		t.Errorf("Modes() = %v, want 47 clear after a full reset", e.Modes())
	}
}

func TestModesTrackDefaultsAndChanges(t *testing.T) {
	e := New(20, 5)
	m := e.Modes()
	if !m[7] || !m[25] {
		t.Fatalf("a fresh emulator should report autowrap (7) and cursor (25) set, got %v", m)
	}
	if m[1] || m[2004] {
		t.Fatalf("a fresh emulator should not report 1 or 2004 set, got %v", m)
	}
	write(t, e, "\x1b[?1h\x1b[?2004h\x1b[?25l")
	m = e.Modes()
	if !m[1] || !m[2004] || m[25] {
		t.Errorf("after setting 1 and 2004 and hiding the cursor, Modes() = %v", m)
	}
	if !e.ApplicationCursorKeys() {
		t.Error("ApplicationCursorKeys should be true after CSI ?1h")
	}
	// ANSI mode 4 (insert) must not be reported as DEC mode 4.
	write(t, e, "\x1b[4h")
	if e.Modes()[4] {
		t.Error("ANSI mode 4 was reported as DEC private mode 4")
	}
}

func TestOnSyncReportsTransitionsOnly(t *testing.T) {
	e := New(20, 5)
	var got []bool
	e.OnSync(func(open bool) { got = append(got, open) })
	write(t, e, "\x1b[?2026h\x1b[?2026hframe\x1b[?2026l\x1b[?2026l")
	want := []bool{true, false}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("OnSync calls = %v, want %v", got, want)
	}
}

// Modes promises only the modes that are set. The emulator's own table lists
// every mode it knows, reset ones as false, so a caller that ranges over the
// map, rather than looking up one mode, would take a reset mode for a set one.
func TestModesListsOnlySetModes(t *testing.T) {
	e := New(20, 4)
	write(t, e, "\x1b[?1000h\x1b[?2004h\x1b[?2004l")
	modes := e.Modes()
	for mode, set := range modes {
		if !set {
			t.Errorf("mode %d is listed although it is reset", mode)
		}
	}
	if !modes[1000] {
		t.Error("mode 1000 was set but is not listed")
	}
	if _, ok := modes[2004]; ok {
		t.Error("mode 2004 was reset but is still listed")
	}
	// A mode reset from the start must not be listed either. The adapter
	// seeds its table from GetModes, which lists reset modes as false.
	if _, ok := e.Modes()[1049]; ok {
		t.Error("mode 1049 was never set but is listed")
	}
}
