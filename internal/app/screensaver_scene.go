package app

import (
	"image/color"
	"math/rand/v2"
	"strings"

	tfx "github.com/Gaurav-Gosain/tuiffects"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/Gaurav-Gosain/tuios/internal/config"
	"github.com/Gaurav-Gosain/tuios/internal/overlay"
	"github.com/Gaurav-Gosain/tuios/internal/theme"
)

// Two kinds of screen saver run behind one interface.
//
// An effect is a tuiffects animation over a capture of the screen: the picture
// of the screen comes apart and reassembles, and when it has reassembled the
// effect is over and the saver builds the next one. A scene is a screen saver
// in the classic sense. It draws a picture of its own, needs nothing from the
// screen it covers, and never ends on its own; the first key takes it down.
//
// The saver and the effect picker's preview drive both through saverAnimation,
// so the idle timer, the busy check, the dismiss on input and the frame chain
// are written once and know nothing about which kind is running.

// saverAnimation is one running screen saver, of either kind.
type saverAnimation interface {
	// Advance steps one frame and reports whether the animation goes on. False
	// means it has run to its end, and the caller builds a fresh one.
	Advance() bool
	// Frame is the whole screen as styled lines, after the last Advance.
	Frame() string
}

// effectAnimation is a tuiffects effect running over a captured screen.
type effectAnimation struct {
	engine *tfx.Engine
	effect tfx.Effect
}

func (a *effectAnimation) Advance() bool { return a.effect.Advance(a.engine) }
func (a *effectAnimation) Frame() string { return a.engine.Frame() }

// saverScene describes one scene: what the picker says about it, and how to
// build it at a size and a frame rate.
type saverScene struct {
	description string
	build       func(width, height, rate int) saverAnimation
}

// saverScenes is every scene by its config name. The names are declared in
// internal/config, which validates the option and lists it in the picker; the
// drawing is here. A name in one table and not the other is a saver that is
// accepted and never starts, which is why newSaverScene is the only reader.
var saverScenes = map[string]saverScene{
	config.ScreensaverSceneMatrix: {
		description: "Green glyphs rain down a black screen, the classic. It runs until you press a key.",
		build:       func(w, h, rate int) saverAnimation { return newMatrixRain(w, h, rate) },
	},
}

// newSaverScene builds the named scene, or returns nil for a name that is not
// one. rate is the frames a second the caller will drive it at, so a scene
// written in seconds moves at the same speed whatever max_fps says.
func newSaverScene(name string, width, height int, s *config.Settings) saverAnimation {
	scene, ok := saverScenes[name]
	if !ok || width < 1 || height < 1 {
		return nil
	}
	return scene.build(width, height, screensaverRate(s))
}

// sceneDescription is the picker's sentence about a scene, or "" for a name
// that is not one.
func sceneDescription(name string) string { return saverScenes[name].description }

// matrixRain is the classic rain: in every column a drop falls, a bright head
// trailing a tail that fades to nothing, and the glyphs behind the head keep
// changing while the tail is over them. The drops respawn above the screen
// after a random wait, so about a third of the columns carry one at a time
// and the rain never reads as a wall.
type matrixRain struct {
	width, height int
	// dt is the seconds one Advance stands for: one over the paint rate.
	dt float64

	drops []rainDrop
	// glyph is the glyph each cell shows while a tail is over it, an index
	// into matrixGlyphs, row-major.
	glyph []uint16
	// shade is the style for each distance behind the head, far enough apart
	// to read as a fade and few enough to downsample to sixteen colours.
	shade [4]uv.Style

	buf   uv.ScreenBuffer
	frame string
}

// rainDrop is one column's drop.
type rainDrop struct {
	// head is the row of the leading glyph, fractional. It is negative while
	// the drop waits above the screen.
	head float64
	// speed is in rows a second.
	speed float64
	// length is the tail, in rows, including the head.
	length int
}

// matrixGlyphs are the characters the rain is made of: half-width katakana, as
// in the film, with digits and a few symbols between them. Every one of them is
// a single cell wide, so a column stays a column.
var matrixGlyphs = func() []string {
	var out []string
	for r := rune(0xFF66); r <= 0xFF9D; r++ {
		out = append(out, string(r))
	}
	for _, r := range "0123456789:=*+-<>\"Z" {
		out = append(out, string(r))
	}
	return out
}()

// Rain tuning, in rows and seconds so the picture is the same at every frame
// rate.
const (
	matrixMinSpeed = 5.0
	matrixMaxSpeed = 16.0
	// matrixMinTail is the shortest tail. The ramp below has four steps, and a
	// tail shorter than that shows no fade.
	matrixMinTail = 4
	// matrixMutations is how many cells under a tail change glyph per second,
	// per column. The classic shimmer is slow: a cell or two a second.
	matrixMutations = 2.0
)

// newMatrixRain starts the rain over an empty screen. The drops begin above
// the top edge at staggered heights, so the screen fills in over the first
// seconds rather than appearing full.
func newMatrixRain(width, height, rate int) *matrixRain {
	r := &matrixRain{
		width:  width,
		height: height,
		dt:     1 / float64(max(rate, 1)),
		drops:  make([]rainDrop, width),
		glyph:  make([]uint16, width*height),
		shade:  matrixShades(),
		buf:    uv.NewScreenBuffer(width, height),
	}
	for i := range r.glyph {
		r.glyph[i] = uint16(rand.IntN(len(matrixGlyphs)))
	}
	for x := range r.drops {
		r.respawn(x)
		// The first drops start anywhere in the wait, not all at its top, or
		// the rain would arrive as one front.
		r.drops[x].head = -rand.Float64() * float64(2*height)
	}
	r.draw()
	return r
}

// matrixShades is the fade from the head back along the tail: white, the
// theme's bright green, its green, and that green sunk half way to its black.
// The colours come from the ANSI palette, so a theme with its own green rains
// in that green, and a terminal with no theme rains in its own.
func matrixShades() [4]uv.Style {
	ansi := theme.GetANSIPalette()
	var dim color.Color = overlay.MixColors(ansi[2], ansi[0], 0.45)
	return [4]uv.Style{
		{Fg: ansi[15], Attrs: uv.AttrBold},
		{Fg: ansi[10], Attrs: uv.AttrBold},
		{Fg: ansi[2]},
		{Fg: dim},
	}
}

// respawn puts a column's drop above the screen with a fresh speed and tail,
// waiting between nothing and two screen heights before its head comes in.
func (r *matrixRain) respawn(x int) {
	maxTail := max(matrixMinTail, r.height*2/3)
	r.drops[x] = rainDrop{
		head:   -float64(rand.IntN(2*r.height + 1)),
		speed:  matrixMinSpeed + rand.Float64()*(matrixMaxSpeed-matrixMinSpeed),
		length: matrixMinTail + rand.IntN(maxTail-matrixMinTail+1),
	}
}

// Advance moves every drop one frame's worth and redraws. The rain never ends,
// so it always reports that it goes on.
func (r *matrixRain) Advance() bool {
	for x := range r.drops {
		d := &r.drops[x]
		before := int(d.head)
		d.head += d.speed * r.dt
		after := int(d.head)
		// The head picks a new glyph for each row it enters, which is what makes
		// the leading character look like it is being typed.
		for y := before + 1; y <= after; y++ {
			if y >= 0 && y < r.height {
				r.glyph[y*r.width+x] = uint16(rand.IntN(len(matrixGlyphs)))
			}
		}
		// A slow shimmer in the tail.
		if rand.Float64() < matrixMutations*r.dt {
			if y := after - rand.IntN(d.length); y >= 0 && y < r.height {
				r.glyph[y*r.width+x] = uint16(rand.IntN(len(matrixGlyphs)))
			}
		}
		if after-d.length >= r.height {
			r.respawn(x)
		}
	}
	r.draw()
	return true
}

// Frame is the screen after the last Advance.
func (r *matrixRain) Frame() string { return r.frame }

// draw writes the drops into the buffer and renders it line by line, the way
// the frame canvas is rendered.
func (r *matrixRain) draw() {
	blank := uv.Cell{Content: " ", Width: 1}
	for y := range r.height {
		line := r.buf.Lines[y]
		for x := range r.width {
			line[x] = blank
		}
	}
	for x := range r.drops {
		d := &r.drops[x]
		head := int(d.head)
		if d.head < 0 {
			continue
		}
		for back := range d.length {
			y := head - back
			if y < 0 {
				break
			}
			if y >= r.height {
				continue
			}
			r.buf.Lines[y][x] = uv.Cell{
				Content: matrixGlyphs[r.glyph[y*r.width+x]],
				Width:   1,
				Style:   r.shade[r.shadeFor(back, d.length)],
			}
		}
	}
	var b strings.Builder
	b.Grow(r.width * r.height * 4)
	for y := range r.height {
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r.buf.Line(y).Render())
	}
	r.frame = b.String()
}

// shadeFor is the ramp step for a cell back rows behind the head of a tail of
// the given length: the head itself, then the brightest quarter, then the
// middle, then the fading end.
func (r *matrixRain) shadeFor(back, length int) int {
	switch {
	case back == 0:
		return 0
	case back*4 < length:
		return 1
	case back*4 < length*3:
		return 2
	default:
		return 3
	}
}
