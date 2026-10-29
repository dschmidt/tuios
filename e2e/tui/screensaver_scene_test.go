package tuie2e

import (
	"strings"
	"testing"
	"time"

	"github.com/Gaurav-Gosain/tuitest"
)

// sceneMarker is the text the shell prints before the saver covers it. The
// scene never shows it: a scene draws its own picture, not one of the screen.
const sceneMarker = "SCENEMARKTEXT"

// hasRain reports whether a screen carries the half-width katakana the matrix
// rain is made of. No effect draws those over a screen of ASCII, so a screen
// with them is the scene and not an effect that happened to run instead.
func hasRain(s tuitest.Screen) bool {
	for _, r := range s.Text() {
		if r >= 0xFF66 && r <= 0xFF9D {
			return true
		}
	}
	return false
}

// TestMatrixSceneRunsUntilAKeyAndSurvivesAResize is the classic screen saver
// next to the effects: cmatrix draws its own rain, keeps drawing it through a
// resize, and comes down on the first key.
//
// The resize is the part an effect cannot do. An effect is built over a
// capture of the screen, and a resize stops it because the capture is of a
// screen that is gone; the marker is back within a frame. The scene holds no
// capture, so it is rebuilt at the new size and the marker stays hidden.
//
// Negative control: with the scene removed, cmatrix is an unknown name and
// falls back to random. No effect draws katakana, so the rain never appears;
// and whichever effect runs, the resize brings the marker back.
func TestMatrixSceneRunsUntilAKeyAndSurvivesAResize(t *testing.T) {
	base := t.TempDir()
	writeConfig(t, base, "[screensaver]\neffect = \"cmatrix\"\n")
	term := startIn(t, base, startOpts{cols: 100, rows: 30})
	waitBoot(t, term)
	newWindow(t, term)
	enterTerminalMode(t, term)
	runInShell(t, term, "echo "+splitMarker(sceneMarker), sceneMarker, shellTimeout)
	leaveTerminalMode(t, term)

	if err := term.SendKeys("S"); err != nil {
		t.Fatalf("press S to start the saver: %v", err)
	}
	if err := term.WaitFor(func(s tuitest.Screen) bool {
		return !strings.Contains(s.Text(), sceneMarker) && hasRain(s)
	}, uiTimeout); err != nil {
		t.Fatalf("the rain never covered the screen: %v\n%s", err, term.Snapshot())
	}

	// It moves. Two frames some way apart must differ, or it is a still.
	first := term.Screen().Text()
	if err := term.WaitFor(func(s tuitest.Screen) bool {
		return hasRain(s) && s.Text() != first
	}, uiTimeout); err != nil {
		t.Fatalf("the rain does not move: %v\n%s", err, term.Snapshot())
	}

	// A resize keeps the scene up, at the new size.
	if err := term.Resize(80, 20); err != nil {
		t.Fatalf("resize: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	s := term.Screen()
	if strings.Contains(s.Text(), sceneMarker) {
		t.Fatalf("the resize took the scene down and gave the screen back\n%s", term.Snapshot())
	}
	if !hasRain(s) {
		t.Fatalf("the rain stopped after the resize\n%s", term.Snapshot())
	}
	if rows := len(strings.Split(strings.TrimRight(s.Text(), "\n"), "\n")); rows > 20 {
		t.Fatalf("the scene is %d rows after a resize to 20\n%s", rows, term.Snapshot())
	}
	t.Logf("the rain after the resize:\n%s", term.Snapshot())

	// The first key takes it down and is not passed on.
	if err := term.SendKeys("escape"); err != nil {
		t.Fatalf("press a key to dismiss the saver: %v", err)
	}
	if err := term.WaitFor(func(s tuitest.Screen) bool {
		return strings.Contains(s.Text(), sceneMarker) && !hasRain(s)
	}, uiTimeout); err != nil {
		t.Fatalf("the key did not dismiss the scene: %v\n%s", err, term.Snapshot())
	}
}
