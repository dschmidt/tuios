package tuie2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// Drag and drop between panes over the kitty drag and drop protocol (OSC 72).
//
// Two tiled panes side by side in a daemon session. A program in the left pane
// offers files; the pointer presses on it, crosses to the right pane and
// releases. What each program received is read from its log, so the assertion
// is on the protocol exchange, not on the screen.
//
// How this could pass wrongly, written down first:
//   - the target could log a drop that never crossed tuios, if the two
//     programs shared a channel. They run in two panes with two PTYs and the
//     only path between them is tuios.
//   - the gesture could be recognised from the harness's own mouse reports
//     reaching the source program, which also turns on mouse reporting. The
//     source only offers after a t=o from tuios, which the harness never
//     writes, and the log records that t=o first.
//   - the fallback paste could come from the harness typing. The harness
//     types nothing into the target pane after starting its reader, and the
//     paste carries paths the harness never typed.
//
// Negative controls: with DnDMotion returning false for every event, the
// source never receives t=o and every subtest fails on its first wait. With
// the MIME list sent on the first move only, the target (which reads it from
// every move, as yazi does) refuses every later move and no drop comes. With
// a new press not ending a stale gesture, the ignored-gesture subtest never
// sees its second drag. See NEGATIVE_CONTROLS.md.

// dndBoard is a daemon session with two tiled panes side by side, in terminal
// mode, and their rectangles.
func dndBoard(t *testing.T) (term *tuitest.Terminal, base, session string, left, right winRect) {
	t.Helper()
	session = "e2e-dnd"
	base = t.TempDir()
	writeConfig(t, base, "[startup]\ndaemon = true\ntiled = true\nlayout = 'bsp'\n")
	// The protocol log follows the suite's own TUIOS_DND_LOG, so a failing
	// exchange can be read back from that file.
	// A fixed cell size makes a pixel report arithmetic; see dndMouse.
	env := []string{"TUIOS_CELL_SIZE=8x16"}
	if v := os.Getenv("TUIOS_DND_LOG"); v != "" {
		env = append(env, "TUIOS_DND_LOG="+v)
	}
	out := &lockedBuffer{}
	term = startIn(t, base, startOpts{cols: 140, rows: 40, args: []string{"new", session}, env: env, out: out})
	dndHostOut.Store(term, out)
	killDaemon(t, base)
	waitBoot(t, term)
	newWindow(t, term)
	newWindow(t, term)
	waitWindowCount(t, term, 2, "dnd board setup")
	rects := waitForSettledGeometryIn(t, base, session, 2)
	left, right = rects[0], rects[1]
	if right.X < left.X {
		left, right = right, left
	}
	if right.X < left.X+left.Width {
		t.Fatalf("the two panes are not side by side: %+v", rects)
	}
	enterTerminalMode(t, term)
	return term, base, session, left, right
}

// runInPane types a command into a pane through the daemon and waits for the
// pane to show marker.
func runInPane(t *testing.T, base, session string, pane winRect, command, marker string) {
	t.Helper()
	if out, err := tuiosCLI(t, base, "send-text", "-s", session, "-w", pane.ID, command+"\n"); err != nil {
		t.Fatalf("send-text to %s: %v\n%s", pane.ID, err, out)
	}
	waitCapture(t, base, session, pane.ID, marker)
}

// dndHostOut is each board's copy of what tuios wrote to its terminal, by
// terminal, so the mouse helpers can tell which form tuios asked for.
var dndHostOut sync.Map

// hostInPixelMode reports whether tuios has put its terminal in SGR-pixel
// mouse mode (1016), which it does while a pane that asked for pixel
// reports is on screen. The dnd kitten is such a pane. A real terminal then
// reports the pointer in pixels; the harness has to as well, or tuios reads
// cell 104 as pixel 104, which is cell 13.
func hostInPixelMode(term *tuitest.Terminal) bool {
	v, ok := dndHostOut.Load(term)
	if !ok {
		return false
	}
	s := v.(*lockedBuffer).String()
	return strings.LastIndex(s, "\x1b[?1016h") > strings.LastIndex(s, "\x1b[?1016l")
}

// dndMouse sends one report at a cell, in pixels at the cell's centre when
// the host is in pixel mode.
func dndMouse(t *testing.T, term *tuitest.Terminal, what string, action tuitest.MouseAction, col, row int) {
	t.Helper()
	ev := tuitest.MouseEvent{Col: col, Row: row, Button: tuitest.MouseLeft, Action: action}
	if hostInPixelMode(term) {
		ev.Col, ev.Row, ev.Pixel = col*8+4, row*16+8, true
	}
	sendMouse(t, term, what, ev)
}

// dragAcross presses in from, carries the pointer a cell at a time to to, and
// returns without releasing. The frame mid-drag is saved as an artifact.
func dragAcross(t *testing.T, term *tuitest.Terminal, fromX, fromY, toX, toY int, art string) {
	t.Helper()
	dndMouse(t, term, "press", tuitest.MousePress, fromX, fromY)
	steps := max(abs(toX-fromX), abs(toY-fromY))
	for i := 1; i <= steps; i++ {
		dndMouse(t, term, "motion", tuitest.MouseDrag, fromX+(toX-fromX)*i/steps, fromY+(toY-fromY)*i/steps)
	}
	time.Sleep(400 * time.Millisecond)
	saveArtifact(t, term, art, "mid-drag")
}

// dropAt releases the button at a cell.
func dropAt(t *testing.T, term *tuitest.Terminal, col, row int) {
	t.Helper()
	dndMouse(t, term, "release", tuitest.MouseRelease, col, row)
}

func TestDnDBetweenPanes(t *testing.T) {
	bin := buildDnDPeer(t)

	t.Run("source to target over the protocol", func(t *testing.T) {
		term, base, session, left, right := dndBoard(t)
		art := artifactDir(t)
		dir := t.TempDir()
		for _, name := range []string{"a.txt", "b.txt"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		srcLog, dstLog := filepath.Join(dir, "source.log"), filepath.Join(dir, "target.log")
		runInPane(t, base, session, right, "stty raw -echo; "+bin+" target "+dstLog, "DNDPEER-READY")
		runInPane(t, base, session, left, fmt.Sprintf("stty raw -echo; %s source %s %s %s", bin, srcLog,
			filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")), "DNDPEER-READY")

		fromX, fromY := left.X+4, left.Y+5
		toX, toY := right.X+right.Width/2, right.Y+right.Height/2
		dragAcross(t, term, fromX, fromY, toX, toY, art)

		// The label names the files and the pane they come from, so the person
		// can check both before letting go.
		if s := term.Screen().Text(); !strings.Contains(s, "a.txt, b.txt") || !strings.Contains(s, "from:") {
			t.Errorf("the drag label is not on screen mid-drag\n%s", term.Snapshot())
		}

		dropAt(t, term, toX, toY)
		got := waitFile(t, dstLog, func(s string) bool { return strings.Contains(s, "data x=1") }, "the target never got the drop data")
		src := waitFile(t, srcLog, func(s string) bool { return strings.Contains(s, "finished") }, "the source never heard the drag finish")
		_ = os.WriteFile(filepath.Join(art, "source.log"), []byte(src), 0o644)
		_ = os.WriteFile(filepath.Join(art, "target.log"), []byte(got), 0o644)

		// The source: the gesture at the pressed cell, the start accepted, the
		// target's acceptance, the drop, the finish.
		for _, want := range []string{
			"recv t=o o=0 x=", "sent offer+start", `recv t=E o=0 x=0 y=0 m=false payload="OK"`,
			"recv t=e o=0 x=1 y=0", "recv t=e o=1 x=2", "recv t=e o=0 x=3", "finished canceled=0",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("the source log lacks %q:\n%s", want, src)
			}
		}
		// The target: a move with the MIME list, the drop, the URI list.
		uris := "file://" + filepath.Join(dir, "a.txt") + "\\r\\nfile://" + filepath.Join(dir, "b.txt")
		for _, want := range []string{
			`recv t=m o=3 x=`, `payload="text/uri-list text/plain"`, "recv t=M o=1", `data x=1 err=<nil> "` + uris + `"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the target log lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "error") {
			t.Errorf("the target was sent an error:\n%s", got)
		}
	})

	t.Run("drop on a pane without the protocol pastes the paths", func(t *testing.T) {
		term, base, session, left, right := dndBoard(t)
		art := artifactDir(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
		srcLog, out := filepath.Join(dir, "source.log"), filepath.Join(dir, "pasted")
		// A reader with bracketed paste on, as a shell or an agent has it: the
		// paths must arrive bracketed, so the program knows they were not typed.
		runInPane(t, base, session, right,
			"stty raw -echo; printf '\\033[?2004h'; printf 'SINK%sREADY\\n' ''; cat > "+out, "SINKREADY")
		runInPane(t, base, session, left, fmt.Sprintf("stty raw -echo; %s source %s %s", bin, srcLog,
			filepath.Join(dir, "one.txt")), "DNDPEER-READY")

		toX, toY := right.X+right.Width/2, right.Y+right.Height/2
		dragAcross(t, term, left.X+4, left.Y+5, toX, toY, art)
		dropAt(t, term, toX, toY)

		want := "\x1b[200~" + filepath.Join(dir, "one.txt") + "\x1b[201~"
		pasted := waitFile(t, out, func(s string) bool { return strings.Contains(s, "\x1b[201~") }, "the pane never got the paste")
		if pasted != want {
			t.Errorf("the pane got %q, want %q", pasted, want)
		}
		src := waitFile(t, srcLog, func(s string) bool { return strings.Contains(s, "finished") }, "the source never heard the drag finish")
		if !strings.Contains(src, "finished canceled=0") {
			t.Errorf("the source was not told the drop succeeded:\n%s", src)
		}
	})

	t.Run("a gesture the program ignored does not block the next", func(t *testing.T) {
		term, base, session, left, right := dndBoard(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
		srcLog, dstLog := filepath.Join(dir, "source.log"), filepath.Join(dir, "target.log")
		runInPane(t, base, session, right, "stty raw -echo; "+bin+" target "+dstLog, "DNDPEER-READY")
		runInPane(t, base, session, left, fmt.Sprintf("stty raw -echo; DNDPEER_IGNORE_GESTURES=1 %s source %s %s", bin, srcLog,
			filepath.Join(dir, "one.txt")), "DNDPEER-READY")
		toX, toY := right.X+right.Width/2, right.Y+right.Height/2
		// The first gesture lands on nothing the program drags: it stays
		// silent, and the button comes up.
		dragAcross(t, term, left.X+4, left.Y+5, left.X+12, left.Y+9, artifactDir(t))
		dropAt(t, term, left.X+12, left.Y+9)
		waitFile(t, srcLog, func(s string) bool { return strings.Contains(s, "ignored gesture") }, "the source never saw the first gesture")
		// The second gesture is a new drag, not a continuation of the first.
		dragAcross(t, term, left.X+4, left.Y+6, toX, toY, artifactDir(t))
		dropAt(t, term, toX, toY)
		waitFile(t, dstLog, func(s string) bool { return strings.Contains(s, "data x=1") }, "the target never got the drop data of the second gesture")
	})

	t.Run("a drop into the source pane is refused", func(t *testing.T) {
		term, base, session, left, _ := dndBoard(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("1"), 0o600); err != nil {
			t.Fatal(err)
		}
		srcLog := filepath.Join(dir, "source.log")
		runInPane(t, base, session, left, fmt.Sprintf("stty raw -echo; %s source %s %s", bin, srcLog,
			filepath.Join(dir, "one.txt")), "DNDPEER-READY")
		toX, toY := left.X+left.Width-6, left.Y+left.Height-6
		dragAcross(t, term, left.X+4, left.Y+5, toX, toY, artifactDir(t))
		dropAt(t, term, toX, toY)
		src := waitFile(t, srcLog, func(s string) bool { return strings.Contains(s, "finished") }, "the source never heard the drag end")
		if !strings.Contains(src, "finished canceled=1") || strings.Contains(src, "recv t=e o=0 x=3") {
			t.Errorf("a drop into the source pane was not cancelled:\n%s", src)
		}
	})
}

// TestDnDRealPrograms runs yazi as the source and the dnd kitten, then a plain
// reader, as the target. It needs the binaries, named by TUIOS_E2E_YAZI and
// TUIOS_E2E_KITTEN, and skips without them: neither is a dependency of the
// suite. The kitten copies a dropped text/uri-list into its directory, which is
// the artifact: the file exists on the other side, made by the kitten alone.
func TestDnDRealPrograms(t *testing.T) {
	yazi, kitten := os.Getenv("TUIOS_E2E_YAZI"), os.Getenv("TUIOS_E2E_KITTEN")
	if yazi == "" {
		t.Skip("TUIOS_E2E_YAZI is not set")
	}

	// yaziIn starts yazi in a pane on dir and returns the screen row and
	// column of the file to drag.
	yaziIn := func(t *testing.T, term *tuitest.Terminal, base, session string, pane winRect, home, dir string) (int, int) {
		t.Helper()
		runInPane(t, base, session, pane, fmt.Sprintf("cd %s && HOME=%s %s", dir, home, yazi), "drag-me.txt")
		time.Sleep(time.Second)
		return findText(t, term, "drag-me.txt")
	}

	t.Run("yazi to the dnd kitten", func(t *testing.T) {
		if kitten == "" {
			t.Skip("TUIOS_E2E_KITTEN is not set")
		}
		term, base, session, left, right := dndBoard(t)
		art := artifactDir(t)
		home, src, dst := t.TempDir(), t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "drag-me.txt"), []byte("hello from yazi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runInPane(t, base, session, right, fmt.Sprintf("cd %s && %s dnd --drop-anywhere=copy", dst, kitten), "transfer the files here")
		row, col := yaziIn(t, term, base, session, left, home, src)

		toX, toY := right.X+right.Width/2, right.Y+right.Height/2
		dragAcross(t, term, col+2, row, toX, toY, art)
		if s := term.Screen().Text(); !strings.Contains(s, "drag-me.txt") || !strings.Contains(s, "from:") {
			t.Errorf("the drag label is not on screen mid-drag\n%s", term.Snapshot())
		}
		dropAt(t, term, toX, toY)
		got := waitFile(t, filepath.Join(dst, "drag-me.txt"), func(s string) bool { return s == "hello from yazi\n" }, "the kitten never copied the file")
		_ = os.WriteFile(filepath.Join(art, "copied.txt"), []byte(got), 0o644)
		saveArtifact(t, term, art, "after-drop")
	})

	t.Run("yazi to a plain reader", func(t *testing.T) {
		term, base, session, left, right := dndBoard(t)
		art := artifactDir(t)
		home, src := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(src, "drag-me.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(home, "pasted")
		runInPane(t, base, session, right,
			"stty raw -echo; printf '\\033[?2004h'; printf 'SINK%sREADY\\n' ''; cat > "+out, "SINKREADY")
		row, col := yaziIn(t, term, base, session, left, home, src)
		toX, toY := right.X+right.Width/2, right.Y+right.Height/2
		dragAcross(t, term, col+2, row, toX, toY, art)
		dropAt(t, term, toX, toY)
		want := "\x1b[200~" + filepath.Join(src, "drag-me.txt") + "\x1b[201~"
		pasted := waitFile(t, out, func(s string) bool { return strings.Contains(s, "\x1b[201~") }, "the pane never got the paste")
		if pasted != want {
			t.Errorf("the pane got %q, want %q", pasted, want)
		}
	})
}

// buildDnDPeer compiles the protocol peer once per test binary.
func buildDnDPeer(t *testing.T) string {
	t.Helper()
	dndPeerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "dndpeer")
		if err != nil {
			dndPeerErr = err
			return
		}
		bin := filepath.Join(dir, "dndpeer")
		build := exec.Command("go", "build", "-o", bin, "./dndpeer")
		if out, err := build.CombinedOutput(); err != nil {
			dndPeerErr = fmt.Errorf("build dndpeer: %v\n%s", err, out)
			return
		}
		dndPeerBin = bin
	})
	if dndPeerErr != nil {
		t.Fatalf("%v", dndPeerErr)
	}
	return dndPeerBin
}

var (
	dndPeerOnce sync.Once
	dndPeerBin  string
	dndPeerErr  error
)

// TestDnDRegistrationOutlivesTheClient: the registration a program makes once,
// when it starts, is kept by the daemon, so a client that attaches later can
// still drag from it and drop on it. And with two clients attached, the program
// hears one answer to its query and only the client that made the drag answers
// for it.
//
// How this could pass wrongly, written down first:
//   - the second client could see the registration because the program sent
//     it again. The peers register once, at start, before that client exists.
//   - the drop could reach the target from the first client, still attached.
//     attach -d takes it off before the drag; the drag is made in the second.
//   - one answer to the query could be the harness's. The harness writes
//     nothing to a pane's input but the drag's mouse reports.
//
// Negative control: with takeDaemonDnD returning at once, the reattach subtest
// fails: the new client never offers the drag. With dndShared reporting false,
// the two-client subtest fails: the peers get two answers to t=q and an EPERM
// from the client that did not make the drag. See NEGATIVE_CONTROLS.md.
func TestDnDRegistrationOutlivesTheClient(t *testing.T) {
	bin := buildDnDPeer(t)

	// peers starts a target in the right pane and a source in the left one.
	peers := func(t *testing.T, base, session string, left, right winRect) (dir, srcLog, dstLog string) {
		t.Helper()
		dir = t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		srcLog, dstLog = filepath.Join(dir, "source.log"), filepath.Join(dir, "target.log")
		runInPane(t, base, session, right, "stty raw -echo; "+bin+" target "+dstLog, "DNDPEER-READY")
		runInPane(t, base, session, left, fmt.Sprintf("stty raw -echo; %s source %s %s", bin, srcLog, filepath.Join(dir, "a.txt")), "DNDPEER-READY")
		return dir, srcLog, dstLog
	}
	// another attaches one more client of the board's size, in terminal mode.
	another := func(t *testing.T, base, session string, args ...string) *tuitest.Terminal {
		t.Helper()
		env := []string{"TUIOS_CELL_SIZE=8x16"}
		if v := os.Getenv("TUIOS_DND_LOG"); v != "" {
			env = append(env, "TUIOS_DND_LOG="+v)
		}
		out := &lockedBuffer{}
		term := startIn(t, base, startOpts{cols: 140, rows: 40, args: append([]string{"attach", session}, args...), env: env, out: out})
		dndHostOut.Store(term, out)
		if err := term.WaitFor(func(s tuitest.Screen) bool { return countWindows(s) == 2 }, bootTimeout); err != nil {
			t.Fatalf("the client never showed the two panes: %v\n%s", err, term.Snapshot())
		}
		windowManagementMode(t, term)
		enterTerminalMode(t, term)
		return term
	}
	count := func(s, sub string) int { return strings.Count(s, sub) }

	t.Run("a client attached after the programs started", func(t *testing.T) {
		_, base, session, left, right := dndBoard(t)
		dir, srcLog, dstLog := peers(t, base, session, left, right)
		// The first client goes; a new one comes, which never saw the peers
		// register.
		term := another(t, base, session, "-d")
		dragAcross(t, term, left.X+4, left.Y+5, right.X+right.Width/2, right.Y+right.Height/2, artifactDir(t))
		dropAt(t, term, right.X+right.Width/2, right.Y+right.Height/2)
		got := waitFile(t, dstLog, func(s string) bool { return strings.Contains(s, "data x=1") }, "the target never got the drop data")
		if !strings.Contains(got, "file://"+filepath.Join(dir, "a.txt")) {
			t.Errorf("the target got the wrong data:\n%s", got)
		}
		src, _ := os.ReadFile(srcLog)
		if !strings.Contains(string(src), "finished canceled=0") {
			t.Errorf("the source was not told the drop succeeded:\n%s", src)
		}
	})

	t.Run("two clients attached", func(t *testing.T) {
		first, base, session, left, right := dndBoard(t)
		second := another(t, base, session)
		_, srcLog, dstLog := peers(t, base, session, left, right)
		// The drag is made in the first client, still in terminal mode from
		// the board; the second one sees every byte the peers write and must
		// stay out of it.
		dragAcross(t, first, left.X+4, left.Y+5, right.X+right.Width/2, right.Y+right.Height/2, artifactDir(t))
		dropAt(t, first, right.X+right.Width/2, right.Y+right.Height/2)
		dst := waitFile(t, dstLog, func(s string) bool { return strings.Contains(s, "data x=1") }, "the target never got the drop data")
		src := waitFile(t, srcLog, func(s string) bool { return strings.Contains(s, "finished") }, "the source never heard the drag finish")
		time.Sleep(time.Second)
		b, _ := os.ReadFile(dstLog)
		dst = string(b)
		b, _ = os.ReadFile(srcLog)
		src = string(b)
		_ = second
		for name, log := range map[string]string{"source": src, "target": dst} {
			if n := count(log, "recv t=q"); n != 1 {
				t.Errorf("the %s got %d answers to its query, want 1:\n%s", name, n, log)
			}
		}
		if n := count(dst, "data x=1"); n != 1 {
			t.Errorf("the target got the data %d times, want once:\n%s", n, dst)
		}
		if strings.Contains(dst, "error") || strings.Contains(src, "EPERM") || strings.Contains(src, "EINVAL") {
			t.Errorf("a client that did not make the drag answered for it:\nsource:\n%s\ntarget:\n%s", src, dst)
		}
		if count(src, `payload="OK"`) != 1 || !strings.Contains(src, "finished canceled=0") {
			t.Errorf("the source did not see one start and a finished drop:\n%s", src)
		}
	})
}
