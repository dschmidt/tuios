#!/bin/bash
# Shared setup for the drag and drop checks against a real kitty under Xvfb.
# See README.md for what each scenario does and what it needs.
set -u

: "${KITTY_BIN:?set KITTY_BIN to the bin directory of kitty 0.49 or later}"
: "${TUIOS_BIN:?set TUIOS_BIN to a tuios binary}"
K=$KITTY_BIN
TUIOS=$TUIOS_BIN
YAZI=${YAZI_BIN:-yazi}
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT=${XDND_OUT:-$(mktemp -d /tmp/tuios-xdnd-XXXX)}

# X11 only: on a Wayland desktop kitty would take WAYLAND_DISPLAY over the
# Xvfb that DISPLAY names, and the GTK helper the same.
unset WAYLAND_DISPLAY
export DISPLAY=${XDND_DISPLAY:-:91} LIBGL_ALWAYS_SOFTWARE=1 GDK_BACKEND=x11
# 1280x800, which the window positions below are laid out for.
xdpyinfo >/dev/null 2>&1 || { Xvfb "$DISPLAY" -screen 0 1280x800x24 >/dev/null 2>&1 & sleep 1; }

W=$OUT/$1
rm -rf "$W"; mkdir -p "$W/src" "$W/dst" "$W/root"
KOPT=(--config NONE -o enable_audio_bell=no -o remember_window_size=no -o linux_display_server=x11)

# An isolated tuios: its own runtime, config and home. The runtime directory
# is short because a unix socket path is limited to 108 bytes.
RUN=$(mktemp -d /tmp/xd-XXXX)
chmod 700 "$RUN"
TENV=(env XDG_RUNTIME_DIR=$RUN XDG_CONFIG_HOME=$W/root/config XDG_STATE_HOME=$W/root/state
      XDG_CACHE_HOME=$W/root/cache XDG_DATA_HOME=$W/root/data HOME=$W/root/home SHELL=/bin/sh
      TUIOS_DND_LOG=$W/dnd.log)
mkdir -p "$W/root/config/tuios" "$W/root/home"
printf "[startup]\ndaemon = true\ntiled = true\nlayout = 'bsp'\n" > "$W/root/config/tuios/config.toml"
tcli() { (cd "$W" && "${TENV[@]}" "$TUIOS" "$@"); }

# win CLASS -> X window id
win() { xdotool search --class "$1" | head -1; }

# geom WID -> "X Y W H"
geom() { xdotool getwindowgeometry --shell "$1" | awk -F= '/^X=/{x=$2}/^Y=/{y=$2}/^WIDTH=/{w=$2}/^HEIGHT=/{h=$2}END{print x, y, w, h}'; }

# drag X1 Y1 X2 Y2: press, move in steps, release, in screen pixels. xdotool
# goes through XTEST, so the X server sees a real pointer and the programs
# speak XDND to each other.
drag() {
  xdotool mousemove "$1" "$2"; sleep 0.3
  xdotool mousedown 1; sleep 0.3
  local steps=40 i
  for i in $(seq 1 $steps); do
    xdotool mousemove $(( $1 + ($3-$1)*i/steps )) $(( $2 + ($4-$2)*i/steps )); sleep 0.04
  done
  sleep 0.8
  xdotool mouseup 1
}

# panes: "ID x y w h NAME" in cells for each pane of the session, from the
# daemon.
panes() { tcli list-windows --json -s dnd | python3 -I -c '
import json,sys
for w in json.load(sys.stdin)["windows"]: print(w["window_id"], w["x"], w["y"], w["width"], w["height"], w["display_name"])'; }

# cellpx FILE NAME DX DY: the screen pixel of cell (DX, DY) inside the pane
# called NAME, from a panes listing in FILE and the window's BX BY CW CH.
cellpx() { awk -v n="$2" -v dx="$3" -v dy="$4" -v bx=$BX -v by=$BY -v cw=$CW -v ch=$CH \
  '$6==n {print int(bx+($2+dx)*cw+cw/2), int(by+($3+dy)*ch+ch/2)}' "$1"; }

# yazipx FILE NAME: a cell on the first file row of yazi's middle column, the
# list it drags from, whatever the pane's width.
yazipx() { awk -v n="$2" -v bx=$BX -v by=$BY -v cw=$CW -v ch=$CH \
  '$6==n {print int(bx+($2+$4*0.4)*cw), int(by+($3+2)*ch+ch/2)}' "$1"; }

# terminal_mode WID: tuios starts in window management mode, where a press is
# tuios's own; in terminal mode it reaches the program in the pane.
terminal_mode() { xdotool windowfocus "$1"; sleep 0.3; xdotool key --window "$1" i; sleep 1; }

cleanup() {
  tcli kill-server >/dev/null 2>&1
  for c in dndA tuiosB gtkdndsource gtkdndtarget; do xdotool search --class $c windowkill 2>/dev/null; done
  rm -rf "$RUN"
}

# result NAME TEST...: prints PASS or FAIL for a scenario's outcome.
result() {
  local name=$1; shift
  if "$@"; then echo "PASS $name"; else echo "FAIL $name"; fi
}
