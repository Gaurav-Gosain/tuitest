// This file drives the tuios daemon control plane and then attaches a real TUI
// to it, proving the two halves meet: state created headlessly over the JSON
// verb protocol must render when a client later attaches.
//
// The headless half speaks the line delimited JSON verb protocol over the
// daemon's unix socket directly, so it exercises the real wire format rather
// than any in-process shortcut. The attach half uses the tuitest harness.
//
// Run with the same setup as the other tuios examples:
//
//	go build -o /tmp/tuios ./cmd/tuios   # from the tuios repo
//	TUIOS_BIN=/tmp/tuios go test ./examples/tuios/...

package tuios_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// ctlEnv is an isolated tuios installation: a binary plus a private set of XDG
// directories, shared by a headless control-plane client and an attached TUI.
type ctlEnv struct {
	t      *testing.T
	bin    string
	env    []string
	socket string
}

// newCtlEnv prepares hermetic XDG directories so the developer's real daemon,
// sessions, and saved state are never touched.
func newCtlEnv(t *testing.T) *ctlEnv {
	t.Helper()
	bin := locateTuios(t)

	base := t.TempDir()
	env := make([]string, 0, 8)
	dirs := map[string]string{}
	for _, key := range []string{
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME",
		"XDG_CACHE_HOME", "XDG_DATA_HOME",
	} {
		dir := filepath.Join(base, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", key, err)
		}
		dirs[key] = dir
		env = append(env, key+"="+dir)
	}
	env = append(env, "SHELL=/bin/sh")

	e := &ctlEnv{
		t:      t,
		bin:    bin,
		env:    env,
		socket: filepath.Join(dirs["XDG_RUNTIME_DIR"], "tuios", "tuios.sock"),
	}
	t.Cleanup(e.killServer)
	return e
}

// run executes a tuios subcommand in the isolated environment.
func (e *ctlEnv) run(args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = append(os.Environ(), e.env...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// killServer stops the daemon and waits for it to finish shutting down. The
// daemon unlinks its socket only after saving state, so socket-file removal is
// the signal that shutdown actually completed.
func (e *ctlEnv) killServer() {
	_, _ = e.run("kill-server")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(e.socket); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// verb sends one JSON verb request over a fresh connection and returns the
// decoded result, failing the test on a protocol error.
func (e *ctlEnv) verb(verb string, params map[string]any) map[string]any {
	e.t.Helper()

	conn, err := net.DialTimeout("unix", e.socket, 5*time.Second)
	if err != nil {
		e.t.Fatalf("dial %s: %v", e.socket, err)
	}
	defer func() { _ = conn.Close() }()

	req := map[string]any{"id": 1, "verb": verb}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		e.t.Fatalf("marshal %s: %v", verb, err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		e.t.Fatalf("write %s: %v", verb, err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	raw, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		e.t.Fatalf("read %s: %v", verb, err)
	}

	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		e.t.Fatalf("decode %s: %v\nraw: %s", verb, err, raw)
	}
	if errObj, ok := resp["error"]; ok {
		e.t.Fatalf("%s returned error: %v", verb, errObj)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		e.t.Fatalf("%s: no result object: %v", verb, resp)
	}
	return res
}

// waitForSocket blocks until the daemon socket accepts a connection.
func (e *ctlEnv) waitForSocket(timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", e.socket, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("daemon socket %s never became connectable", e.socket)
}

// TestAttachRendersHeadlessState is the crossover test: build up session state
// with no client attached, then attach a real TUI and assert that what the
// daemon owns is what the user sees. A window created headlessly and a command
// run headlessly must both be visible on the attached screen.
func TestAttachRendersHeadlessState(t *testing.T) {
	t.Parallel()
	e := newCtlEnv(t)

	// 1. Create a headless session; no TUI has ever been attached to it. Not
	//    every tuios build carries 'new --detach', and without it there is no
	//    way to reach the state this test is about, so skip rather than fail on
	//    a binary that predates (or postdates) the flag.
	if out, err := e.run("new", "--detach", "cross"); err != nil {
		if strings.Contains(out, "Unknown flag: --detach") {
			t.Skipf("this tuios build has no 'new --detach', so headless session creation is unavailable:\n%s", out)
		}
		t.Fatalf("new --detach: %v\n%s", err, out)
	}
	e.waitForSocket(10 * time.Second)

	// 2. Create a distinctively named window purely over the control plane.
	created := e.verb("new-window", map[string]any{"session": "cross", "name": "ctlwin"})
	winID, _ := created["window_id"].(string)
	if winID == "" {
		t.Fatalf("new-window returned no window_id: %v", created)
	}

	// 3. Run a command in it headlessly. The value is computed by the shell, so
	//    the marker proves real execution rather than an echo of our bytes.
	e.verb("send-text", map[string]any{
		"session": "cross",
		"window":  winID,
		"text":    "echo crossmarker-$((21*2))\n",
	})
	waited := e.verb("wait-for", map[string]any{
		"condition": "window-output",
		"session":   "cross",
		"window":    winID,
		"pattern":   "crossmarker-42",
		"timeout":   20000,
	})
	if matched, _ := waited["matched"].(bool); !matched {
		t.Fatalf("headless command never produced output: %v", waited)
	}

	// 4. Now attach a real TUI to that session, in the same isolated
	//    environment so it finds the same daemon.
	term := tuitest.StartT(t, []string{e.bin, "attach", "cross"},
		tuitest.WithSize(120, 40),
		tuitest.WithTerm("xterm-256color"),
		tuitest.WithEnv(e.env...),
	)

	// 5. The headlessly created window's name must render in the attached UI,
	//    proving the daemon's state reached the client.
	if err := term.WaitForText("ctlwin", 15*time.Second); err != nil {
		t.Fatalf("headless window name never rendered after attach: %v\n--- screen ---\n%s",
			err, term.Screen().Text())
	}

	// 6. The pane content produced headlessly must render too, proving the
	//    client re-subscribed to the live daemon-owned PTY rather than starting
	//    a blank one.
	if err := term.WaitForText("crossmarker-42", 15*time.Second); err != nil {
		t.Fatalf("headless pane output never rendered after attach: %v\n--- screen ---\n%s",
			err, term.Screen().Text())
	}

	// 7. The attached client must agree with the control plane about window
	//    count, i.e. attaching did not fork a second view of the world.
	listed := e.verb("list-windows", map[string]any{"session": "cross"})
	if total := int(listed["total"].(float64)); total < 2 {
		t.Fatalf("expected the initial window plus ctlwin, got %d", total)
	}

	// 8. Killing the session over the control plane must destroy it in the
	//    daemon and evict the attached client.
	e.verb("kill-session", map[string]any{"session": "cross"})

	sessions := e.verb("list-sessions", nil)
	for _, entry := range sessions["sessions"].([]any) {
		s, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := s["name"].(string); name == "cross" {
			t.Fatalf("killed session still listed: %v", sessions["sessions"])
		}
	}

	// 9. The attached client must exit rather than linger in a dead UI. Its
	//    session no longer exists, so there is nothing to render and nothing to
	//    reconnect to; a client that stays alive here leaves the user typing
	//    into a session that is gone.
	//
	//    This previously hung: the daemon dropped the session without telling
	//    its clients, so the process stayed up until it was killed by hand.
	code, err := term.Wait(20 * time.Second)
	if err != nil {
		t.Fatalf("attached client did not exit after its session was killed: %v\n--- screen ---\n%s",
			err, term.Screen().Text())
	}

	// 10. And it must exit non-zero: the session was destroyed underneath the
	//     client, which is not the same outcome as the user detaching, and a
	//     script driving tuios has to be able to tell them apart.
	if code == 0 {
		t.Errorf("client exited 0 after its session was killed; a kill is not a clean detach\n--- screen ---\n%s",
			term.Screen().Text())
	}

	// 11. The reason must be on screen, so a human sees what happened rather
	//     than an empty prompt.
	if screen := term.Screen().Text(); !strings.Contains(screen, "terminated") {
		t.Errorf("client exited without explaining why:\n--- screen ---\n%s", screen)
	}
}
