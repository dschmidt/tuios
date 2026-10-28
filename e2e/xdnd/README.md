# Drag and drop against a real kitty, on X11 and on Wayland

These scripts check the bridge between tuios and a host terminal that speaks
the kitty drag and drop protocol (OSC 72; `internal/app/dnd_host.go`). They
run kitty 0.49 twice: under Xvfb with real X11 drags made by `xdotool`, and
natively on Wayland under a headless sway, with drags made through a virtual
pointer (`wayland/`). kitty, GTK and the programs in the panes speak XDND or
the Wayland data device protocol to each other as they do on a desktop.
The drags between panes that tuios carries itself, with no host
terminal involved, are covered by `e2e/tui/dnd_osc72_test.go`.

They are not part of `go test`: they need a display server, kitty, yazi and
GTK, none of which the suite depends on.

| Script | What is dragged | Passes when |
| --- | --- | --- |
| `s1_inbound.sh` | `kitten dnd FILE` in another kitty window, into a tuios pane running `kitten dnd`, then into a pane running a plain reader | the kitten in tuios copies the file; the reader gets the path as a bracketed paste |
| `s2_outbound.sh kitten`, `s2_outbound.sh yazi` | `kitten dnd FILE` or yazi in a tuios pane, out to `kitten dnd` in another kitty window | the outside kitten copies the file |
| `s3_same_window.sh` | yazi in one tuios pane, to `kitten dnd` in another pane of the same window, while kitty carries the drag | the file is copied, kitty reported the drop to tuios, and tuios asked kitty for no data (kitty refuses a drop into the window a drag came from) |
| `s4_gtk.sh` | a GTK 3 window (`gtkdnd.py`) into a tuios pane, and yazi in a tuios pane out to a GTK window | the kitten copies the file; GTK receives the `file://` URI |

`run_all.sh` runs them all and prints one PASS or FAIL line per check. Each
scenario's directory under `$XDND_OUT` keeps the protocol exchange as tuios
saw it (`dnd.log`, written because the scripts set `TUIOS_DND_LOG`), and
kitty's own logs.

```bash
KITTY_BIN=/path/to/kitty/bin TUIOS_BIN=/path/to/tuios YAZI_BIN=/path/to/yazi \
  GTK_PYTHON=python3 bash e2e/xdnd/run_all.sh
```

Needs: Xvfb, `xdotool`, `xdpyinfo`, the X11 libraries kitty loads
(`libxcb-xkb1`, `libxkbcommon-x11-0` and the Mesa GL drivers on Ubuntu), kitty
0.49 or later from its release tarball, yazi 26.9 or later, and GTK 3
bindings for Python (`python3-gi`, `gir1.2-gtk-3.0`). `GTK_PYTHON` names the
Python those bindings were built for. `XDND_DISPLAY` picks the display, `:91`
by default; one is started at 1280x800 when none answers.

## Wayland

`wayland/run_all.sh` runs the same nine checks natively on Wayland. It starts
a headless sway of its own (one 1280x800 output, no Xwayland, so nothing can
fall back to X11) and builds `vpointer`, a virtual pointer on sway's seat
(`wlr-virtual-pointer-unstable-v1`). Headless sway has no input device, and a
seat without a pointer gives the windows no pointer events, so sway's own
`seat cursor` commands reach nothing. The tuios session and its panes are
made first, detached, and the client in kitty attaches to them: an attached
client starts in terminal mode, where a press reaches the program in a pane.

```bash
KITTY_BIN=/path/to/kitty/bin TUIOS_BIN=/path/to/tuios YAZI_BIN=/path/to/yazi \
  GTK_PYTHON=python3 bash e2e/xdnd/wayland/run_all.sh
```

Needs, besides the list above without the X11 parts: `sway`, `gcc`,
`wayland-scanner` and the libwayland-client headers (`libwayland-bin`,
`libwayland-dev`), and `curl` to fetch the protocol file once.

## What a change to the bridge has to keep

Three things learned while writing them, which a change to the bridge has to
keep:

- kitty takes a move it sent with no answer of its own as a refusal. tuios
  answers every move, even when nothing changed.
- The MIME list goes with every move, not with the first only as the
  specification allows. kitty sends it every time, and yazi reads it from
  every move: an empty list is "nothing to drop" to it.
- kitty recognises the drag gesture itself, whether or not the program in its
  window tracks the mouse, and sends `t=o` on any left drag in the window.
  tuios hands it on only when the press went to a pane that offers drags, so
  in window management mode, where tuios keeps the press, nothing starts.
- kitty reports a drag that comes back over the window it started in like any
  other, and would refuse its data with EPERM. tuios serves it from the source
  pane and only tells kitty the drop is done.

And one about the checks themselves: without the bridge, kitty pastes a drop
as text into its window, which tuios hands to the focused pane. The drop on
the plain reader is made with the other pane focused, so only the bridge,
which routes by the pointer, can put the path there.
