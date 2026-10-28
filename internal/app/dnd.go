package app

import (
	"fmt"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Gaurav-Gosain/tuios/internal/debuglog"
	"github.com/Gaurav-Gosain/tuios/internal/session"
	"github.com/Gaurav-Gosain/tuios/internal/terminal"
	"tuios.dev/vt"
)

// Drag and drop between panes over the kitty drag and drop protocol (OSC 72;
// the wire is in vt/dnd.go).
//
// To the program in a pane, tuios is the terminal: the program says it offers
// drags (t=o:x=1) or takes drops (t=a), and tuios sends it the gesture, the
// moves, the drop and the data. Between two panes tuios plays the part the
// window system plays under kitty: it carries the drag from the pane it
// started in to the pane under the pointer, asks the source for data when
// the target asks, and tells each side how the other answered. No host
// terminal takes part, so this works in any terminal, and only between the
// panes of this client.
//
// The gesture is kitty's: a left press in a pane whose program offers drags,
// then the pointer moving to another cell. The press and the moves before
// that reach the program as mouse events, as in kitty. The release does not,
// since kitty's native drag consumes it and the programs expect that. The
// program decides whether the press was on something to drag: tuios offers,
// the program answers with MIME types or stays silent.
//
// A pane whose program did not register for drops still takes one: the paths
// of a dropped text/uri-list arrive as a paste, quoted the way a terminal
// pastes a dropped file, bracketed when the program asked for that. A drop
// from yazi onto a shell or an agent in another pane works with nothing in
// that pane knowing the protocol.
//
// tuios reads no file in this mode. The source is asked for its data and the
// target gets exactly that; the target program opens paths with its own
// rights. A drop into the pane the drag came from is refused with EPERM, as
// kitty refuses it.
//
// Not here yet: the bridge to a host terminal that speaks the protocol, drops
// onto a pane whose shell runs on another machine (which should go through
// the file transfer DropPaste uses), and registrations kept in the daemon
// rather than in each client.

// DnDMsg is one message a pane's program sent, chunk by chunk.
type DnDMsg struct {
	WindowID string
	Cmd      vt.DnDCommand
}

// dndWakeMsg says the inbox has messages to drain.
type dndWakeMsg struct{}

// dndInbox is where the emulator callbacks leave messages for the update
// loop. The callback runs on the PTY reader under the window's IO lock, so it
// must not block, and a data stream is thousands of chunks, which a channel
// of a fixed size would lose in the middle. So: a bounded queue under its own
// lock, and a one-slot wake channel.
type dndInbox struct {
	mu      sync.Mutex
	msgs    []DnDMsg
	bytes   int
	dropped bool
	wake    chan struct{}
}

// dndInboxCap bounds the payload bytes waiting in the inbox.
const dndInboxCap = 32 << 20

func (q *dndInbox) push(msg DnDMsg) {
	q.mu.Lock()
	if q.bytes+len(msg.Cmd.Payload) > dndInboxCap {
		q.dropped = true
	} else {
		q.msgs = append(q.msgs, msg)
		q.bytes += len(msg.Cmd.Payload)
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *dndInbox) drain() (msgs []DnDMsg, dropped bool) {
	q.mu.Lock()
	msgs, dropped = q.msgs, q.dropped
	q.msgs, q.bytes, q.dropped = nil, 0, false
	q.mu.Unlock()
	return msgs, dropped
}

// ensureDnDInbox makes the inbox on first use.
func (m *OS) ensureDnDInbox() *dndInbox {
	if m.dndIn == nil {
		m.dndIn = &dndInbox{wake: make(chan struct{}, 1)}
	}
	return m.dndIn
}

// listenDnDUntil wakes the update loop when a pane left a message. The
// caller re-arms it after each wake.
func listenDnDUntil(stop <-chan struct{}, q *dndInbox) tea.Cmd {
	return listenOnce(stop, q.wake, func(struct{}) tea.Msg { return dndWakeMsg{} })
}

// setupDnD wires a window's OSC 72 to the inbox. Called under the window's
// IO lock from installPassthroughs.
func (m *OS) setupDnD(window *terminal.Window) {
	if window == nil {
		return
	}
	q := m.ensureDnDInbox()
	id := window.ID
	window.DnDFunc = func(cmd vt.DnDCommand) {
		q.push(DnDMsg{WindowID: id, Cmd: cmd})
	}
}

// handleDnDWake drains the inbox and acts on each message.
func (m *OS) handleDnDWake() {
	msgs, dropped := m.ensureDnDInbox().drain()
	if dropped {
		// A stream with a hole in it is not data anyone should get.
		m.dndCancel("the drag and drop inbox overflowed")
	}
	for _, msg := range msgs {
		m.onDnD(msg)
	}
}

// dndPane is what one pane's program has said about itself, and the chunk
// assembler and data streams of that pane.
type dndPane struct {
	asm vt.DnDAssembler
	// accepts says the program sent t=a; acceptMimes is the list it gave.
	accepts     bool
	acceptMimes []string
	// offers says the program sent t=o:x=1.
	offers bool
	// id is the multiplexer id (i key) the program used, echoed back on
	// everything sent to it.
	id int
	// machineID is what the program sent with t=a:x=1 or t=o:x=1. Unused
	// yet: every pane of this client counts as local.
	machineID string
	// streams are the data streams in progress from this pane, by type and
	// index: pre-sent data (p), item data for a request (e), and the text
	// drag image (p with a negative index, keyed by it).
	streams map[dndStreamKey]*dndStream
}

type dndStreamKey struct {
	typ byte
	idx int
}

// dndStream is base64 text accumulating until the chunk that ends it.
type dndStream struct {
	enc []byte
}

// dndMaxStreamBytes bounds one stream's encoded bytes. The specification
// asks a terminal to take 64 MB of pre-sent data; this is a proof of
// concept, and what crosses it is paths.
const dndMaxStreamBytes = 16 << 20

// dndPaneFor is the record for a window, made on first use.
func (m *OS) dndPaneFor(id string) *dndPane {
	if m.dndPanes == nil {
		m.dndPanes = map[string]*dndPane{}
	}
	p := m.dndPanes[id]
	if p == nil {
		p = &dndPane{streams: map[dndStreamKey]*dndStream{}}
		m.dndPanes[id] = p
	}
	return p
}

// dndWindowClosed forgets a pane that is gone and ends a drag it took part
// in.
func (m *OS) dndWindowClosed(id string) {
	delete(m.dndPanes, id)
	if m.dndPress != nil && m.dndPress.window == id {
		m.dndPress = nil
	}
	if d := m.dndDrag; d != nil && (d.source == id || d.target == id || d.dropTarget == id) {
		if d.source == id {
			d.source = ""
		}
		if d.target == id {
			d.target = ""
		}
		if d.dropTarget == id {
			d.dropTarget = ""
		}
		m.dndCancel("a pane of the drag closed")
	}
}

// dndLog writes a line to the debug log when TUIOS_DEBUG_INTERNAL is set,
// as the kitty passthrough does, or to the file TUIOS_DND_LOG names, which
// the e2e suite sets because the general debug mode changes what the dock
// shows. Every message in and out goes through it, which is how an exchange
// with a program is read back.
func dndLog(format string, args ...any) {
	path := os.Getenv("TUIOS_DND_LOG")
	if path == "" {
		if os.Getenv("TUIOS_DEBUG_INTERNAL") != "1" {
			return
		}
		path = debuglog.Path
	}
	f, err := debuglog.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "[%s] DND: %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// takeDaemonDnD takes the drag and drop registration the daemon kept for a
// pane, for a pane this client has heard nothing from itself: it attached
// after the program registered, which a program does once, when it starts.
// Once the client has a record of its own, the stream it reads is fresher
// than any state push. See session/dnd_registration.go.
func (m *OS) takeDaemonDnD(w *terminal.Window, ws *session.WindowState) {
	if w == nil || ws == nil || (!ws.DnDAccepts && !ws.DnDOffers) {
		return
	}
	if _, seen := m.dndPanes[w.ID]; seen {
		return
	}
	p := m.dndPaneFor(w.ID)
	p.accepts, p.acceptMimes, p.offers, p.id = ws.DnDAccepts, append([]string(nil), ws.DnDMimes...), ws.DnDOffers, ws.DnDID
	dndLog("took registration of %s from the daemon: accepts=%v offers=%v", shortID(w.ID), p.accepts, p.offers)
}

// dndShared reports whether a pane's program may be heard by more than this
// client: its PTY is the daemon's, and another client can be attached. Such a
// client answers only what belongs to a drag of its own, and leaves the
// query to the daemon. A message for another client's drag gets no error
// from this one.
func (m *OS) dndShared(id string) bool {
	w := m.windowByID(id)
	return w != nil && w.DaemonMode
}

// dndSend writes one message to a pane's program, with the pane's
// multiplexer id.
func (m *OS) dndSend(id string, cmd vt.DnDCommand) {
	w := m.windowByID(id)
	if w == nil {
		return
	}
	if p := m.dndPanes[id]; p != nil && cmd.ID == 0 {
		cmd.ID = p.id
	}
	dndLog("send %s t=%c o=%d x=%d y=%d X=%d Y=%d payload=%q", shortID(id), cmd.Type, cmd.Op, cmd.X, cmd.Y, cmd.PX, cmd.PY, cmd.Payload)
	_ = w.SendInput(cmd.Encode())
}

// dndSendData writes a data response to a pane's program.
func (m *OS) dndSendData(id string, hdr vt.DnDCommand, data []byte) {
	w := m.windowByID(id)
	if w == nil {
		return
	}
	if p := m.dndPanes[id]; p != nil && hdr.ID == 0 {
		hdr.ID = p.id
	}
	dndLog("send %s t=%c x=%d y=%d data=%d bytes", shortID(id), hdr.Type, hdr.X, hdr.Y, len(data))
	_ = w.SendInput(vt.EncodeDnDData(hdr, data))
}

// dndError writes an error message of type t (R to a drop target, E to a
// drag source) to a pane's program.
func (m *OS) dndError(id string, t byte, x, y int, errno, why string) {
	m.dndSend(id, vt.DnDCommand{Type: t, X: x, Y: y, Payload: vt.DnDErrorPayload(errno, why)})
}

// onDnD acts on one message from a pane's program.
func (m *OS) onDnD(msg DnDMsg) {
	p := m.dndPaneFor(msg.WindowID)
	cmd, ok := p.asm.Add(msg.Cmd)
	if !ok {
		return
	}
	if cmd.ID != 0 {
		p.id = cmd.ID
	}
	id := msg.WindowID
	dndLog("recv %s t=%c o=%d x=%d y=%d X=%d Y=%d m=%v payload=%q", shortID(id), cmd.Type, cmd.Op, cmd.X, cmd.Y, cmd.PX, cmd.PY, cmd.More, cmd.Payload)
	switch cmd.Type {
	case 'q':
		// Yes, this terminal speaks the protocol. The payload names optional
		// parts; there are none yet. A daemon's pane hears it from the daemon.
		if !m.dndShared(id) {
			m.dndSend(id, vt.DnDCommand{Type: 'q', ID: cmd.ID})
		}
	case 'a':
		if cmd.X == 1 {
			p.machineID = string(cmd.Payload)
			return
		}
		p.accepts = true
		p.acceptMimes = vt.DnDMimeList(cmd.Payload)
	case 'A':
		p.accepts = false
		if d := m.dndDrag; d != nil && d.dropTarget == id {
			m.dndCancel("the drop target stopped taking drops")
		}
	case 'o':
		m.onDnDOffer(id, p, cmd)
	case 'p':
		m.onDnDPresent(id, p, cmd)
	case 'P':
		m.onDnDStart(id, cmd)
	case 'e':
		m.onDnDItemData(id, p, cmd)
	case 'E':
		m.onDnDSourceError(id, cmd)
	case 'm':
		m.onDnDTargetStatus(id, cmd)
	case 'r':
		m.onDnDRequest(id, cmd)
	}
}
