package app

import (
	"net/url"
	"path"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Gaurav-Gosain/tuios/internal/overlay"
	"github.com/Gaurav-Gosain/tuios/internal/terminal"
	"github.com/Gaurav-Gosain/tuios/internal/theme"
	"tuios.dev/vt"
)

// dndPhase is where a drag is in its life.
type dndPhase int

const (
	// dndOffered: the gesture was seen and t=o went to the program; it has
	// not answered yet.
	dndOffered dndPhase = iota + 1
	// dndBuilding: the program answered with its MIME types and may be
	// pre-sending data and an image.
	dndBuilding
	// dndStarted: the program sent t=P:x=-1 and the drag follows the pointer.
	dndStarted
	// dndDropped: t=M went to a target, which may now request data.
	dndDropped
)

// dndPress is a left press forwarded to a pane whose program offers drags.
// It is the only way a drag can start: the gesture needs a button held in
// the pane the offer goes to.
type dndPress struct {
	window       string
	x, y         int // layout cell of the press
	termX, termY int // the same cell inside the pane
}

// dndDrag is one drag, from the gesture to the finish.
type dndDrag struct {
	phase  dndPhase
	source string
	// released says the button came up before the program started the drag;
	// a start that arrives after that is refused with EPERM.
	released bool
	// flags is the operations the source allows (1 copy, 2 move, 3 either)
	// and mimes the types it offers, in its order.
	flags int
	mimes []string
	// presented is data the source sent ahead, decoded, by MIME index.
	presented map[int][]byte
	// icon is the text drag image the source gave, cleaned for the label.
	icon string
	// x and y are where the pointer is, in layout cells.
	x, y int
	// target is the pane under the pointer that was sent t=m, or "". op is
	// its last answer (0 until it answers), and wants the types it asked for.
	target string
	op     int
	wants  []string
	// told is whether the source has been told the target accepts.
	told bool
	// dropTarget is the pane the drop went to; requests from it are served.
	// pending marks MIME indexes whose data was asked of the source for it.
	dropTarget string
	pending    map[int]bool
	// fallback says the drop went to a pane without the protocol, which gets
	// the paths as a paste once the data for fallbackIdx is in hand.
	fallback    bool
	fallbackIdx int
	// fromHost says the drag came in from outside the tuios window, through a
	// host terminal that speaks the protocol. It has no source pane; the
	// host serves its data. See dnd_host.go.
	fromHost bool
	// relay says the drag started in a pane and runs as the window system's
	// drag, through the host terminal. The host owns the pointer and tells
	// the source how the drag goes; tuios passes the messages on.
	relay bool
	// hostPending marks MIME indexes whose data the host asked the source
	// for (relay), or tuios asked the host for (fromHost).
	hostPending map[int]bool
	// hostBuf collects the host's data for a fallback paste (fromHost).
	hostBuf []byte
}

// viaHost reports whether the host terminal carries this drag: it came from
// outside, or it left as the window system's drag.
func (d *dndDrag) viaHost() bool { return d.fromHost || d.relay }

// dndMaxMimes bounds the MIME list a source may offer.
const dndMaxMimes = 64

// DnDNotePress records a left press tuios forwarded to a pane. It is the arm
// of the gesture; nothing is sent until the pointer moves.
func (m *OS) DnDNotePress(w *terminal.Window, x, y, termX, termY int) {
	m.dndPress = nil
	if w == nil {
		return
	}
	if p := m.dndPanes[w.ID]; p == nil || !p.offers {
		return
	}
	if d := m.dndDrag; d != nil {
		if d.phase >= dndStarted {
			// A drag is in flight; a second press belongs to no gesture.
			return
		}
		// A gesture the program never took up (a press on nothing it drags)
		// is over with the next press, or it would block every later one.
		m.dndDrag = nil
	}
	m.dndPress = &dndPress{window: w.ID, x: x, y: y, termX: termX, termY: termY}
	dndLog("press in %s at cell (%d,%d)", shortID(w.ID), termX, termY)
}

// DnDActive reports whether motion events must reach the handler for a drag:
// one is in flight, or a press is waiting to become one.
func (m *OS) DnDActive() bool {
	return m.dndPress != nil || m.dndDrag != nil
}

// DnDDragging reports whether a drag follows the pointer, which is when the
// label is drawn and the pointer belongs to the drag.
func (m *OS) DnDDragging() bool {
	return m.dndDrag != nil && m.dndDrag.phase >= dndStarted
}

// DnDMotion takes a pointer motion in layout cells. It reports whether the
// drag consumed it, in which case no pane gets it.
func (m *OS) DnDMotion(x, y int, button tea.MouseButton) bool {
	d := m.dndDrag
	if d != nil && d.viaHost() {
		// The host terminal carries this drag and reports where it is.
		return false
	}
	if d != nil {
		d.x, d.y = x, y
	}
	if button == tea.MouseNone {
		// No button held: a release went missing. The gesture ends here.
		if m.dndPress != nil {
			m.dndPress = nil
		}
		if d != nil && d.phase >= dndStarted && d.phase < dndDropped {
			m.dndCancel("the pointer was released out of sight")
			return true
		}
		if d != nil && d.phase < dndStarted {
			d.released = true
		}
		return false
	}
	if d != nil && d.phase == dndStarted {
		m.dndHover(x, y)
		return true
	}
	if d != nil && d.phase == dndDropped {
		// The drop is done; the pointer is the target's again.
		return false
	}
	press := m.dndPress
	if press == nil || d != nil || button != tea.MouseLeft {
		return false
	}
	if x == press.x && y == press.y {
		return false
	}
	if m.dndHostOn() {
		// The host terminal recognises the gesture and starts a real drag
		// with the window system; it tells tuios with a t=o. See dnd_host.go.
		return false
	}
	// The gesture: the button held in a pane that offers drags, and the
	// pointer in another cell. The program is told where the press was,
	// which is the thing the person took hold of.
	w := m.windowByID(press.window)
	if w == nil {
		m.dndPress = nil
		return false
	}
	px := m.dndPixel(w, press.termX, press.termY)
	m.dndDrag = &dndDrag{phase: dndOffered, source: press.window, x: x, y: y, presented: map[int][]byte{}}
	m.dndSend(press.window, vt.DnDCommand{Type: 'o', X: press.termX, Y: press.termY, PX: px.X, PY: px.Y})
	return false
}

// dndPixel is the pixel inside a pane for one of its cells, as the protocol
// reports beside the cell: what the host reported when it reports pixels,
// otherwise the cell's corner from the pane's cell size.
func (m *OS) dndPixel(w *terminal.Window, termX, termY int) vt.MousePixel {
	if px := m.PointerPixelIn(w.CellPixelWidth, w.CellPixelHeight, termX, termY); px.OK {
		return px
	}
	return vt.MousePixel{X: termX * max(w.CellPixelWidth, 0), Y: termY * max(w.CellPixelHeight, 0)}
}

// DnDRelease takes the button coming up. It reports whether the drag
// consumed the release, in which case no pane gets it.
func (m *OS) DnDRelease(x, y int) bool {
	m.dndPress = nil
	d := m.dndDrag
	if d == nil || d.viaHost() {
		return false
	}
	d.x, d.y = x, y
	switch d.phase {
	case dndOffered, dndBuilding:
		// The program has not started the drag; it may still answer, and
		// the answer will be refused. The release is the pane's.
		d.released = true
		return false
	case dndStarted:
		m.dndDrop(x, y)
		return true
	}
	return false
}

// dndTargetAt is the pane under a layout cell with the cell inside it, or
// nil when the cell is on no pane's content.
func (m *OS) dndTargetAt(x, y int) (*terminal.Window, int, int) {
	idx := m.WindowAt(x, y)
	if idx < 0 {
		return nil, 0, 0
	}
	w := m.Windows[idx]
	tx, ty, ok := w.ScreenToTerminal(x, y)
	if !ok {
		return nil, 0, 0
	}
	return w, tx, ty
}

// dndHover routes a move of a started drag: the pane that lost the pointer
// is told the drag left, the pane under it is told where the drag is. Only
// a pane whose program registered for drops is told anything; another is a
// fallback target and learns of the drop as a paste.
func (m *OS) dndHover(x, y int) {
	d := m.dndDrag
	w, tx, ty := m.dndTargetAt(x, y)
	id := ""
	if w != nil {
		id = w.ID
	}
	dndLog("hover screen=(%d,%d) -> pane=%s cell=(%d,%d)", x, y, shortID(id), tx, ty)
	if id != d.target {
		m.dndLeaveTarget()
		d.target = id
		if d.viaHost() {
			m.dndHostTargetChanged(w)
		}
		if w == nil {
			return
		}
		if p := m.dndPanes[id]; p != nil && p.accepts {
			// Every move carries the MIME list, as kitty's do. The
			// specification allows sending it once, but yazi reads the
			// list from each move and takes an empty one as no types to
			// drop, so it refused every move after the first. The pane
			// answers with what it wants (onDnDTargetStatus).
			px := m.dndPixel(w, tx, ty)
			m.dndSend(id, vt.DnDCommand{Type: 'm', X: tx, Y: ty, PX: px.X, PY: px.Y, Op: d.flags,
				Payload: []byte(strings.Join(d.mimes, " "))})
		}
		return
	}
	if w == nil {
		return
	}
	if p := m.dndPanes[id]; p != nil && p.accepts {
		px := m.dndPixel(w, tx, ty)
		m.dndSend(id, vt.DnDCommand{Type: 'm', X: tx, Y: ty, PX: px.X, PY: px.Y, Op: d.flags,
			Payload: []byte(strings.Join(d.mimes, " "))})
	}
}

// dndLeaveTarget tells the pane the pointer was over that the drag left it,
// and forgets its answer.
func (m *OS) dndLeaveTarget() {
	d := m.dndDrag
	if d.target == "" {
		return
	}
	if p := m.dndPanes[d.target]; p != nil && p.accepts {
		m.dndSend(d.target, vt.DnDCommand{Type: 'm', X: -1, Y: -1})
	}
	if d.told && !d.viaHost() {
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 2, Op: 0})
	}
	d.target, d.op, d.wants, d.told = "", 0, nil, false
}

// dndDrop is the release of a started drag.
func (m *OS) dndDrop(x, y int) {
	d := m.dndDrag
	m.dndHover(x, y)
	w, tx, ty := m.dndTargetAt(x, y)
	if w == nil || w.ID == d.source {
		// Nothing under the pointer, or the pane the drag came from. kitty
		// refuses the data of such a drop; here the drop never happens.
		m.dndCancel("")
		return
	}
	p := m.dndPanes[w.ID]
	if p == nil || !p.accepts {
		m.dndDropFallback(w)
		return
	}
	if d.op == 0 {
		// The program did not accept the drop where the pointer is.
		m.dndCancel("")
		return
	}
	m.dndDeliverDrop(w, tx, ty)
}

// dndDeliverDrop sends the drop to a pane that speaks the protocol and
// accepted it.
func (m *OS) dndDeliverDrop(w *terminal.Window, tx, ty int) {
	d := m.dndDrag
	d.phase = dndDropped
	d.dropTarget = w.ID
	d.pending = map[int]bool{}
	if !d.viaHost() {
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 3})
	}
	px := m.dndPixel(w, tx, ty)
	m.dndSend(w.ID, vt.DnDCommand{Type: 'M', X: tx, Y: ty, PX: px.X, PY: px.Y, Op: d.op,
		Payload: []byte(strings.Join(d.mimes, " "))})
}

// dndDropFallback is a drop on a pane whose program does not speak the
// protocol: its paths, or its text, go in as a paste. The data is asked of
// the source if it was not sent ahead.
func (m *OS) dndDropFallback(w *terminal.Window) {
	d := m.dndDrag
	idx := dndMimeIndex(d.mimes, "text/uri-list")
	if idx < 0 {
		idx = dndMimeIndex(d.mimes, "text/plain")
	}
	if idx < 0 {
		m.dndCancel("")
		return
	}
	d.phase = dndDropped
	d.dropTarget = w.ID
	d.fallback, d.fallbackIdx = true, idx
	d.pending = map[int]bool{}
	if d.fromHost {
		// The data is the host's to give.
		d.hostPending[idx] = true
		d.hostBuf = nil
		m.dndHostSend(vt.DnDCommand{Type: 'r', X: idx + 1})
		return
	}
	if !d.relay {
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 3})
	}
	if data, ok := d.presented[idx]; ok {
		m.dndPasteFallback(data)
		return
	}
	d.pending[idx] = true
	m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 5, Y: idx})
}

// dndPasteFallback pastes dropped data into the fallback target and
// finishes the drag.
func (m *OS) dndPasteFallback(data []byte) {
	d := m.dndDrag
	w := m.windowByID(d.dropTarget)
	text := ""
	if d.mimes[d.fallbackIdx] == "text/uri-list" {
		text = dndPasteTextForURIs(string(data))
	} else {
		text = vt.SanitizePaste(string(data))
	}
	op := 0
	if w != nil && text != "" && m.PasteIntoWindow(w, text) {
		op = 1
	}
	m.dndFinish(op)
}

// dndMimeIndex is the index of mime in mimes, or -1.
func dndMimeIndex(mimes []string, mime string) int {
	for i, m := range mimes {
		if m == mime {
			return i
		}
	}
	return -1
}

// dndFinish ends a drag after a drop. op is the operation the target took,
// 0 when it took nothing.
func (m *OS) dndFinish(op int) {
	d := m.dndDrag
	if d == nil {
		return
	}
	if d.viaHost() {
		// The host ends the window system's drop. For a relay the host then
		// tells the source the drag finished, and the drag ends there.
		m.dndHostSend(vt.DnDCommand{Type: 'r', Op: op})
		if d.relay {
			d.phase, d.dropTarget, d.fallback, d.target = dndStarted, "", false, ""
			return
		}
		m.dndDrag = nil
		return
	}
	y := 1
	if op != 0 {
		y = 0
	}
	if d.source != "" {
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 4, Y: y})
	}
	m.dndDrag = nil
	m.dndPress = nil
}

// dndCancel ends a drag at any phase: the target is told the drag left, or
// the drop failed, and the source that it was cancelled. why is logged when
// given.
func (m *OS) dndCancel(why string) {
	d := m.dndDrag
	if d == nil {
		return
	}
	if why != "" {
		m.LogInfo("Drag and drop ended: %s", why)
	}
	switch {
	case d.phase == dndDropped && !d.fallback && d.dropTarget != "":
		m.dndError(d.dropTarget, 'R', 0, 0, "ECANCELED", "the drag ended")
	case d.phase == dndStarted:
		m.dndLeaveTarget()
	}
	switch {
	case d.fromHost && d.phase == dndDropped:
		m.dndHostSend(vt.DnDCommand{Type: 'r'})
	case d.relay:
		// Cancel the window system's drag too.
		m.dndHostSend(vt.DnDCommand{Type: 'E', Y: -1})
	}
	if d.source != "" && d.phase >= dndStarted {
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 4, Y: 1})
	}
	m.dndDrag = nil
	m.dndPress = nil
}

// onDnDOffer is a t=o from a pane: a registration (x=1 start, x=2 stop) or,
// with the operations and a MIME list, the program taking up the gesture.
func (m *OS) onDnDOffer(id string, p *dndPane, cmd vt.DnDCommand) {
	switch cmd.X {
	case 1:
		p.offers = true
		p.machineID = string(cmd.Payload)
		return
	case 2:
		p.offers = false
		if d := m.dndDrag; d != nil && d.source == id {
			m.dndCancel("the source stopped offering drags")
		}
		return
	}
	d := m.dndDrag
	if d == nil || d.source != id || d.phase > dndBuilding {
		// No gesture asked for this, or the drag is past building. kitty
		// takes an offer at any time; here the button has to be held in the
		// pane, which is the one check the protocol leaves to the terminal.
		// The gesture may be another client's, which answers it.
		if !m.dndShared(id) {
			m.dndError(id, 'E', 0, 0, "EPERM", "no drag gesture in this pane")
		}
		return
	}
	if d.released {
		m.dndError(id, 'E', 0, 0, "EPERM", "the button was released")
		m.dndDrag = nil
		return
	}
	mimes := vt.DnDMimeList(cmd.Payload)
	if len(mimes) == 0 || len(mimes) > dndMaxMimes || cmd.Op < 1 || cmd.Op > 3 {
		m.dndError(id, 'E', 0, 0, "EINVAL", "an offer needs operations and MIME types")
		m.dndDrag = nil
		return
	}
	d.phase = dndBuilding
	d.flags = cmd.Op
	d.mimes = append(d.mimes[:0], mimes...)
	if d.relay {
		m.dndHostSend(vt.DnDCommand{Type: 'o', Op: cmd.Op, Payload: []byte(strings.Join(mimes, " "))})
	}
}

// onDnDPresent is a chunk of t=p: data sent ahead for a MIME type (x >= 0),
// or a drag image (x < 0; only a text image, y=0, is kept).
func (m *OS) onDnDPresent(id string, p *dndPane, cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || d.source != id || d.phase != dndBuilding {
		if !m.dndShared(id) {
			m.dndError(id, 'E', 0, 0, "EINVAL", "no drag is being built")
		}
		return
	}
	if cmd.X >= 0 && cmd.X >= len(d.mimes) {
		m.dndError(id, 'E', 0, 0, "EINVAL", "pre-sent data index out of range")
		m.dndDrag = nil
		return
	}
	if d.relay {
		// The host draws the image and serves data sent ahead, so it gets
		// every chunk as the program sent it.
		fwd := cmd
		fwd.ID = 0
		m.dndHostSend(fwd)
	}
	if cmd.X < 0 && cmd.Y != 0 {
		// A bitmap image. tuios draws text, so it keeps none.
		return
	}
	data, done, ok := m.dndStreamAdd(id, p, 'p', cmd)
	if !ok {
		m.dndError(id, 'E', 0, 0, "EFBIG", "too much pre-sent data")
		m.dndDrag = nil
		return
	}
	if !done {
		return
	}
	if cmd.X >= 0 {
		d.presented[cmd.X] = data
		return
	}
	if cmd.X == -1 {
		d.icon = dndLabelText(string(data), 40)
	}
}

// dndStreamAdd appends a chunk to a pane's stream for (typ, cmd.X) and
// returns the decoded data when the chunk ended it. ok is false past the
// size bound, and the stream is dropped.
func (m *OS) dndStreamAdd(_ string, p *dndPane, typ byte, cmd vt.DnDCommand) (data []byte, done, ok bool) {
	key := dndStreamKey{typ: typ, idx: cmd.X}
	if typ == 'e' {
		key.idx = cmd.Y
	}
	s := p.streams[key]
	if s == nil {
		s = &dndStream{}
		p.streams[key] = s
	}
	if len(s.enc)+len(cmd.Payload) > dndMaxStreamBytes {
		delete(p.streams, key)
		return nil, false, false
	}
	s.enc = append(s.enc, cmd.Payload...)
	if cmd.More || len(cmd.Payload) > 0 {
		return nil, false, true
	}
	delete(p.streams, key)
	data, err := vt.DecodeDnDPayload(s.enc)
	if err != nil {
		return nil, true, false
	}
	return data, true, true
}

// onDnDStart is t=P: x=-1 starts the drag, another x picks an image.
func (m *OS) onDnDStart(id string, cmd vt.DnDCommand) {
	d := m.dndDrag
	if cmd.X >= 0 {
		if d != nil && d.relay && d.source == id {
			m.dndHostSend(vt.DnDCommand{Type: 'P', X: cmd.X})
		}
		return
	}
	if d == nil || d.source != id || d.phase != dndBuilding {
		if !m.dndShared(id) {
			m.dndError(id, 'E', 0, 0, "EINVAL", "no drag is being built")
		}
		return
	}
	if d.released {
		m.dndError(id, 'E', 0, 0, "EPERM", "the button was released")
		m.dndDrag = nil
		return
	}
	// Pre-sent data still open is complete now: the start says everything
	// the program meant to send ahead has been sent. yazi closes its URI
	// list with the empty message but not its text image, and kitty takes
	// pre-sent data with no close at all, so the start is the one end every
	// program agrees on.
	if p := m.dndPanes[id]; p != nil {
		for key, s := range p.streams {
			if key.typ != 'p' {
				continue
			}
			delete(p.streams, key)
			data, err := vt.DecodeDnDPayload(s.enc)
			if err != nil {
				continue
			}
			switch {
			case key.idx >= 0 && key.idx < len(d.mimes):
				d.presented[key.idx] = data
			case key.idx == -1:
				d.icon = dndLabelText(string(data), 40)
			}
		}
	}
	if d.relay {
		// The host starts the window system's drag and answers OK or an
		// error, which goes to the program as the host's answer.
		m.dndHostSend(vt.DnDCommand{Type: 'P', X: -1})
		return
	}
	m.dndSend(id, vt.DnDCommand{Type: 'E', Payload: []byte("OK")})
	d.phase = dndStarted
	m.dndHover(d.x, d.y)
}

// onDnDItemData is a chunk of t=e from the source: data for the MIME type
// at y, asked for with t=e:x=5. It goes to the pane that asked, or into the
// fallback paste.
func (m *OS) onDnDItemData(id string, p *dndPane, cmd vt.DnDCommand) {
	d := m.dndDrag
	if d != nil && d.relay && d.source == id && d.hostPending[cmd.Y] {
		// The host asked for it, for the window system's drop target.
		m.dndHostSend(vt.DnDCommand{Type: 'e', Y: cmd.Y, More: cmd.More, Payload: cmd.Payload})
		if !cmd.More && len(cmd.Payload) == 0 {
			delete(d.hostPending, cmd.Y)
		}
		return
	}
	if d == nil || d.source != id || d.phase != dndDropped || cmd.Y < 0 || cmd.Y >= len(d.mimes) {
		if !m.dndShared(id) {
			m.dndError(id, 'E', 0, cmd.Y, "EINVAL", "no request is open for this data")
		}
		return
	}
	data, done, ok := m.dndStreamAdd(id, p, 'e', cmd)
	if !ok {
		m.dndError(id, 'E', 0, cmd.Y, "EFBIG", "too much data")
		m.dndCancel("the source sent too much data")
		return
	}
	if !done {
		return
	}
	d.presented[cmd.Y] = data
	if !d.pending[cmd.Y] {
		return
	}
	delete(d.pending, cmd.Y)
	if d.fallback {
		if cmd.Y == d.fallbackIdx {
			m.dndPasteFallback(data)
		}
		return
	}
	m.dndSendData(d.dropTarget, vt.DnDCommand{Type: 'r', X: cmd.Y + 1}, data)
}

// onDnDSourceError is t=E from the source: y=-1 cancels the drag, another y
// is an error for that item's data, passed to the target.
func (m *OS) onDnDSourceError(id string, cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || d.source != id {
		return
	}
	if d.relay && (cmd.Y == -1 || d.hostPending[cmd.Y]) {
		m.dndHostSend(vt.DnDCommand{Type: 'E', Y: cmd.Y, Payload: cmd.Payload})
		if cmd.Y == -1 {
			m.dndDrag = nil
		}
		return
	}
	if cmd.Y == -1 || d.phase != dndDropped {
		m.dndCancel("the source cancelled the drag")
		return
	}
	errno, _, _ := strings.Cut(string(cmd.Payload), ":")
	if errno == "" {
		errno = "EUNKNOWN"
	}
	if !d.fallback && d.dropTarget != "" {
		m.dndError(d.dropTarget, 'R', cmd.Y+1, 0, errno, "the source could not provide the data")
	}
	m.dndFinish(0)
}

// onDnDTargetStatus is t=m from the pane under the pointer: the operation it
// would perform (0 for none) and the MIME types it wants. The source hears
// of the first acceptance and of each change.
func (m *OS) onDnDTargetStatus(id string, cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || d.phase != dndStarted || d.target != id {
		return
	}
	if len(cmd.Payload) > 0 {
		d.wants = vt.DnDMimeList(cmd.Payload)
	}
	op := cmd.Op
	if op < 0 || op > 2 {
		op = 0
	}
	if op == d.op {
		return
	}
	d.op = op
	if d.viaHost() {
		// The window system shows whether the drop is accepted, so the host
		// hears the pane's answer as tuios's own.
		m.dndHostAnswer()
		return
	}
	if op != 0 && !d.told {
		idx := 0
		if len(d.wants) > 0 {
			if i := dndMimeIndex(d.mimes, d.wants[0]); i >= 0 {
				idx = i
			}
		}
		m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 1, Y: idx})
		d.told = true
	}
	m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 2, Op: op})
}

// onDnDRequest is t=r from a pane: with x, a request for the data of the
// MIME type at that 1-based index; without, the end of the drop with the
// operation taken.
func (m *OS) onDnDRequest(id string, cmd vt.DnDCommand) {
	d := m.dndDrag
	if d == nil || d.phase != dndDropped || d.dropTarget != id || d.fallback {
		// The specification says this error alone ends nothing. The drop may
		// be another client's, which serves it.
		if !m.dndShared(id) {
			m.dndError(id, 'R', cmd.X, cmd.Y, "EPERM", "drop data can only be requested after a drop")
		}
		return
	}
	if cmd.X == 0 {
		m.dndFinish(cmd.Op)
		return
	}
	idx := cmd.X - 1
	switch {
	case idx < 0 || idx >= len(d.mimes):
		m.dndError(id, 'R', cmd.X, cmd.Y, "ENOENT", "no such MIME type in the drop")
		m.dndFinish(0)
	case d.source == id:
		m.dndError(id, 'R', cmd.X, cmd.Y, "EPERM", "cannot drop into self window")
		m.dndFinish(0)
	case cmd.Y != 0 || cmd.PY != 0:
		// The contents of a file behind a URI. tuios reads no file for a
		// pane; the target opens the path itself.
		m.dndError(id, 'R', cmd.X, cmd.Y, "EINVAL", "file contents are not served between panes")
	case d.fromHost:
		// The host has the data; its answer comes back chunk by chunk and
		// goes on to the pane as it arrives (onHostData).
		d.hostPending[idx] = true
		m.dndHostSend(vt.DnDCommand{Type: 'r', X: cmd.X})
	default:
		if data, ok := d.presented[idx]; ok {
			m.dndSendData(id, vt.DnDCommand{Type: 'r', X: cmd.X}, data)
			return
		}
		if !d.pending[idx] {
			d.pending[idx] = true
			m.dndSend(d.source, vt.DnDCommand{Type: 'e', X: 5, Y: idx})
		}
	}
}

// dndPasteTextForURIs is the paste for a dropped text/uri-list: each
// file:// URI on this machine as a quoted path, any other URI as itself,
// space separated. A URI naming another host stays a URI: the path is not
// one here.
func dndPasteTextForURIs(list string) string {
	var parts []string
	for line := range strings.SplitSeq(list, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := dndLocalFilePath(line); ok {
			parts = append(parts, pastePathText(p))
			continue
		}
		parts = append(parts, vt.SanitizePaste(line))
	}
	return strings.Join(parts, " ")
}

// dndLocalFilePath is the path of a file:// URI with no host or localhost.
func dndLocalFilePath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.Path == "" {
		return "", false
	}
	if strings.ContainsRune(u.Path, 0) || !strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	return u.Path, true
}

// dndURINames are the file names a text/uri-list names, for the label.
func dndURINames(list string) []string {
	var names []string
	for line := range strings.SplitSeq(list, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := dndLocalFilePath(line); ok {
			names = append(names, path.Base(p))
			continue
		}
		names = append(names, line)
	}
	return names
}

// dndLabelText cleans text from a program for the label: printable, one
// line, at most n cells.
func dndLabelText(s string, n int) string {
	return overlay.Truncate(strings.Join(strings.Fields(printableTitle(s)), " "), n)
}

// renderDnDLabel is the label that follows the pointer while a drag is in
// flight: what is being dragged, by the names in the data when the source
// sent it ahead, and the pane it comes from. The program's own image text
// comes last. The names and the source come first because they are the two
// things the person has to be able to check: a program in a pane can offer
// any path and call it anything.
func (m *OS) renderDnDLabel() *lipgloss.Layer {
	d := m.dndDrag
	if d == nil || d.phase != dndStarted || d.viaHost() {
		// A drag the host carries is drawn by the window system.
		return nil
	}
	pal := theme.UI()
	renderW := m.GetRenderWidth()

	what := ""
	if idx := dndMimeIndex(d.mimes, "text/uri-list"); idx >= 0 {
		if data, ok := d.presented[idx]; ok {
			names := dndURINames(string(data))
			switch {
			case len(names) == 0:
				what = "no files"
			case len(names) <= 3:
				what = strings.Join(names, ", ")
			default:
				what = strings.Join(names[:3], ", ") + " +" + strconv.Itoa(len(names)-3)
			}
		} else {
			what = "files"
		}
	} else if len(d.mimes) > 0 {
		what = d.mimes[0]
	}
	what = dndLabelText(what, 48)

	from := "a pane"
	if w := m.windowByID(d.source); w != nil {
		from = dndPaneName(w)
	}
	text := what + " — from: " + from
	if d.icon != "" {
		text += "  " + d.icon
	}
	label := tooltipLabel(text, max(renderW-2, 1), pal)

	x, y := d.x+2, d.y+1
	if y >= m.viewReserve().Top+m.ViewUsableHeight() {
		y = d.y - 1
	}
	return tooltipLayer(label, x, y, renderW, "dnd-label")
}

// dndPaneName names a pane for the label: its machine first when its shell
// runs elsewhere, then its name or title.
func dndPaneName(w *terminal.Window) string {
	name := ""
	switch {
	case w.CustomName != "":
		name = printableTitle(w.CustomName)
	case w.Title() != "" && !isDefaultTitle(w.Title(), w.ID):
		name = printableTitle(w.Title())
	default:
		name = "pane " + shortID(w.ID)
	}
	if w.Host != "" {
		name = w.Host + ":" + name
	}
	return overlay.Truncate(name, 32)
}
