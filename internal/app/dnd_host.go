package app

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/Gaurav-Gosain/tuios/internal/terminal"
	"tuios.dev/vt"
)

// The bridge to a host terminal that speaks the drag and drop protocol.
//
// tuios asks its terminal at startup (t=q). A terminal that answers gets
// tuios registered as a program that takes drops and offers drags, and from
// then on tuios is a client of the host's protocol as well as the terminal of
// its panes:
//
//   - A drag that comes in from outside, a file manager or a browser, reaches
//     tuios as moves and a drop. tuios routes them to the pane under the
//     pointer exactly as it routes its own drags, and passes the pane's data
//     requests to the host and the host's data back. A pane without the
//     protocol gets the paths as a paste, which tuios asks the host for.
//   - A drag that starts in a pane becomes the window system's drag. The host
//     recognises the gesture and sends a t=o; tuios passes it to the pane the
//     button was pressed in, and passes the pane's offer, its data sent
//     ahead, its image and the start to the host. While the drag is out,
//     every event the host sends goes to the source pane, and the data the
//     host asks for comes from it.
//   - The window system's drag that comes back over the tuios window is
//     tuios's own. The host reports it like any other drag, but would refuse
//     to serve its data (a drop into the window a drag came from is EPERM),
//     so tuios serves it from the source pane itself and only tells the host
//     the drop is done.
//
// With the bridge on, tuios does not recognise the gesture itself: the host
// does, and a drag that tuios started on its own at the same time would be
// the same drag twice.
//
// tuios sends the host no machine id, so the host treats every pane as local
// and gives paths, not file contents. A pane whose program runs on another
// machine is a later step (DropPaste and drop-files).

// dndHostState is the bridge's state.
type dndHostState struct {
	asked, supported, registered bool
	asm                          vt.DnDAssembler
	// out is what waits to be written to the host. Both entry points, a
	// pane's message and the host's, run in Update and return it as a
	// command.
	out []byte
}

// dndHostQuery asks the terminal whether it speaks the protocol.
const dndHostQuery = "\x1b]72;t=q\x1b\\"

// dndHostOn reports whether the bridge is up.
func (m *OS) dndHostOn() bool { return m.dndHost.registered }

// dndHostProbe asks the terminal at startup. Only a terminal tuios writes to
// directly is asked: a browser draws through tuios-web, and an ssh client's
// terminal is on another machine, whose paths mean nothing here.
func (m *OS) dndHostProbe() tea.Cmd {
	if m.dndHost.asked || m.LearnMode || m.Client != ClientLocal {
		return nil
	}
	m.dndHost.asked = true
	dndLog("host probe sent")
	return tea.Raw(dndHostQuery)
}

// dndHostSend queues one message for the host.
func (m *OS) dndHostSend(cmd vt.DnDCommand) {
	cmd.ID = 0
	dndLog("host send t=%c o=%d x=%d y=%d X=%d Y=%d m=%v payload=%q", cmd.Type, cmd.Op, cmd.X, cmd.Y, cmd.PX, cmd.PY, cmd.More, cmd.Payload)
	m.dndHost.out = append(m.dndHost.out, cmd.Encode()...)
}

// dndHostFlush returns the command that writes what is queued for the host.
func (m *OS) dndHostFlush() tea.Cmd {
	if len(m.dndHost.out) == 0 {
		return nil
	}
	out := string(m.dndHost.out)
	m.dndHost.out = nil
	return tea.Raw(out)
}

// DnDHostClear is what unregisters tuios from its terminal, or "" when it
// never registered. The caller writes it once the program has stopped, so
// the shell the terminal goes back to is not sent drops it never asked for.
func (m *OS) DnDHostClear() string {
	if !m.dndHost.registered {
		return ""
	}
	m.dndHost.registered = false
	return string(vt.DnDCommand{Type: 'A'}.Encode()) + string(vt.DnDCommand{Type: 'o', X: 2}.Encode())
}

// handleHostDnD takes an OSC 72 the host terminal sent. It reports whether
// msg was one.
func (m *OS) handleHostDnD(msg tea.Msg) (tea.Cmd, bool) {
	ev, ok := msg.(uv.UnknownOscEvent)
	if !ok {
		return nil, false
	}
	body, ok := strings.CutPrefix(string(ev), "\x1b]72;")
	if !ok {
		return nil, false
	}
	body = strings.TrimSuffix(strings.TrimSuffix(body, "\x1b\\"), "\x07")
	if cmd, ok := vt.ParseDnD([]byte(body)); ok {
		if cmd, ok := m.dndHost.asm.Add(cmd); ok {
			dndLog("host recv t=%c o=%d x=%d y=%d X=%d Y=%d m=%v payload=%q", cmd.Type, cmd.Op, cmd.X, cmd.Y, cmd.PX, cmd.PY, cmd.More, cmd.Payload)
			m.onHostDnD(cmd)
		}
	}
	return m.dndHostFlush(), true
}

// onHostDnD acts on one message from the host.
func (m *OS) onHostDnD(cmd vt.DnDCommand) {
	switch cmd.Type {
	case 'q':
		if !m.dndHost.asked || m.dndHost.supported {
			return
		}
		m.dndHost.supported = true
		m.dndHost.registered = true
		m.dndHostSend(vt.DnDCommand{Type: 'a', Payload: []byte("text/uri-list text/plain")})
		m.dndHostSend(vt.DnDCommand{Type: 'o', X: 1})
		m.LogInfo("Host terminal speaks the drag and drop protocol: bridging drags")
	case 'm':
		m.onHostMove(cmd)
	case 'M':
		m.onHostDrop(cmd)
	case 'r':
		m.onHostData(cmd)
	case 'R':
		m.onHostDataError(cmd)
	case 'o':
		m.onHostGesture()
	case 'e':
		m.onHostDragEvent(cmd)
	case 'E':
		m.onHostDragError(cmd)
	}
}

// onHostMove is a drag moving over the tuios window, or leaving it.
func (m *OS) onHostMove(cmd vt.DnDCommand) {
	d := m.dndDrag
	if cmd.X == -1 && cmd.Y == -1 {
		if d == nil || !d.viaHost() || d.phase != dndStarted {
			return
		}
		m.dndLeaveTarget()
		if d.fromHost {
			// The specification says to forget a drag that left undropped.
			m.dndDrag = nil
		}
		return
	}
	if d == nil {
		d = &dndDrag{phase: dndStarted, fromHost: true, presented: map[int][]byte{}, hostPending: map[int]bool{}}
		m.dndDrag = d
	} else if !d.viaHost() || d.phase != dndStarted {
		// tuios's own drag between panes, or a drop being served.
		return
	}
	if d.fromHost && len(cmd.Payload) > 0 {
		d.mimes = vt.DnDMimeList(cmd.Payload)
	}
	if d.fromHost && cmd.Op != 0 {
		d.flags = cmd.Op
	}
	d.x, d.y, _ = m.PointerToLayout(cmd.X, cmd.Y)
	m.dndHover(d.x, d.y)
	m.dndHostAnswer()
}

// dndHostTargetChanged resets tuios's answer to the host when the pointer
// reaches another pane. A pane with the protocol answers for itself
// (onDnDTargetStatus); until it does, the answer is no. A pane without it
// takes a drop of paths or text, as a paste.
func (m *OS) dndHostTargetChanged(w *terminal.Window) {
	d := m.dndDrag
	d.op, d.wants = 0, nil
	if w == nil || w.ID == d.source {
		return
	}
	if p := m.dndPanes[w.ID]; p != nil && p.accepts {
		return
	}
	for _, mime := range []string{"text/uri-list", "text/plain"} {
		if dndMimeIndex(d.mimes, mime) >= 0 {
			d.op, d.wants = 1, []string{mime}
			return
		}
	}
}

// dndHostAnswer tells the host whether tuios takes the drop where the
// pointer is, and which types it wants. It answers every move: kitty takes a
// move with no answer of its own as a refusal, and the program that started
// the drag then drops nothing, as it would for a program run in kitty
// directly that stayed silent.
func (m *OS) dndHostAnswer() {
	d := m.dndDrag
	if d.op == 0 {
		m.dndHostSend(vt.DnDCommand{Type: 'm'})
		return
	}
	m.dndHostSend(vt.DnDCommand{Type: 'm', Op: d.op, Payload: []byte(strings.Join(d.wants, " "))})
}

// onHostDrop is the drop on the tuios window.
func (m *OS) onHostDrop(cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || !d.viaHost() || d.phase != dndStarted {
		m.dndHostSend(vt.DnDCommand{Type: 'r'})
		return
	}
	if d.fromHost && len(cmd.Payload) > 0 {
		d.mimes = vt.DnDMimeList(cmd.Payload)
	}
	d.x, d.y, _ = m.PointerToLayout(cmd.X, cmd.Y)
	m.dndHover(d.x, d.y)
	w, tx, ty := m.dndTargetAt(d.x, d.y)
	refuse := func() {
		m.dndLeaveTarget()
		m.dndHostSend(vt.DnDCommand{Type: 'r'})
		if d.fromHost {
			m.dndDrag = nil
		}
	}
	if w == nil || w.ID == d.source {
		refuse()
		return
	}
	if p := m.dndPanes[w.ID]; p == nil || !p.accepts {
		m.dndDropFallback(w)
		return
	}
	if d.op == 0 {
		refuse()
		return
	}
	m.dndDeliverDrop(w, tx, ty)
}

// onHostData is a chunk of the host's answer to a data request of a drop
// from outside: passed to the pane that asked, or kept for a paste.
func (m *OS) onHostData(cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || !d.fromHost || d.phase != dndDropped || !d.hostPending[cmd.X-1] {
		return
	}
	end := !cmd.More && len(cmd.Payload) == 0
	if d.fallback {
		if !end {
			if len(d.hostBuf)+len(cmd.Payload) > dndMaxStreamBytes {
				m.dndCancel("the dropped data is too large")
				return
			}
			d.hostBuf = append(d.hostBuf, cmd.Payload...)
			return
		}
		data, err := vt.DecodeDnDPayload(d.hostBuf)
		if err != nil {
			m.dndFinish(0)
			return
		}
		m.dndPasteFallback(data)
		return
	}
	if end {
		delete(d.hostPending, cmd.X-1)
	}
	m.dndSend(d.dropTarget, vt.DnDCommand{Type: 'r', X: cmd.X, Y: cmd.Y, PX: cmd.PX, PY: cmd.PY, More: cmd.More, Payload: cmd.Payload})
}

// onHostDataError is the host refusing a data request of a drop from outside.
func (m *OS) onHostDataError(cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || !d.fromHost || d.phase != dndDropped {
		return
	}
	if d.fallback {
		m.dndFinish(0)
		return
	}
	m.dndSend(d.dropTarget, vt.DnDCommand{Type: 'R', X: cmd.X, Y: cmd.Y, Payload: cmd.Payload})
}

// onHostGesture is the host's drag gesture in the tuios window. It goes to
// the pane the left button was pressed in, if that pane offers drags; any
// other gesture is left unanswered, which starts nothing. The host reports
// where the gesture fired, which is past the press by its threshold; the
// pane is told where the press was, which is what the person took hold of.
func (m *OS) onHostGesture() {
	press := m.dndPress
	if d := m.dndDrag; d != nil && d.relay && d.phase < dndStarted {
		m.dndDrag = nil
	}
	if press == nil || m.dndDrag != nil {
		return
	}
	w := m.windowByID(press.window)
	if w == nil {
		return
	}
	if p := m.dndPanes[w.ID]; p == nil || !p.offers {
		return
	}
	m.dndPress = nil
	px := m.dndPixel(w, press.termX, press.termY)
	m.dndDrag = &dndDrag{phase: dndOffered, source: w.ID, relay: true, x: press.x, y: press.y,
		presented: map[int][]byte{}, hostPending: map[int]bool{}}
	m.dndSend(w.ID, vt.DnDCommand{Type: 'o', X: press.termX, Y: press.termY, PX: px.X, PY: px.Y})
}

// onHostDragEvent is an event of the window system's drag of a pane's offer:
// accepted, operation changed, dropped, finished, or a request for data. It
// goes to the source pane as the host sent it.
func (m *OS) onHostDragEvent(cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || !d.relay {
		return
	}
	if cmd.X == 5 {
		d.hostPending[cmd.Y] = true
	}
	m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: cmd.X, Y: cmd.Y, Op: cmd.Op})
	if cmd.X == 4 {
		m.dndDrag = nil
	}
}

// onHostDragError is the host's answer to the start of a pane's drag, OK or
// an error, or an error that ended it. It goes to the source pane.
func (m *OS) onHostDragError(cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || !d.relay {
		return
	}
	m.dndSend(d.source, vt.DnDCommand{Type: 'E', Y: cmd.Y, Payload: cmd.Payload})
	if string(cmd.Payload) == "OK" && d.phase == dndBuilding {
		d.phase = dndStarted
		return
	}
	m.dndDrag = nil
}
