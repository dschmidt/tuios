#!/bin/bash
# Shared setup for the drag and drop checks under headless sway, on native
# Wayland. See ../README.md. run_all.sh starts sway and builds vpointer first,
# and exports what this file reads.
set -u

: "${KITTY_BIN:?set KITTY_BIN to the bin directory of kitty 0.49 or later}"
: "${TUIOS_BIN:?set TUIOS_BIN to a tuios binary}"
: "${XDND_WL_RUNTIME:?run the scenarios through run_all.sh}"
K=$KITTY_BIN
TUIOS=$TUIOS_BIN
YAZI=${YAZI_BIN:-yazi}
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT=${XDND_OUT:?run the scenarios through run_all.sh}

export XDG_RUNTIME_DIR=$XDND_WL_RUNTIME
export WAYLAND_DISPLAY=${XDND_WL_DISPLAY:-wayland-1}
export SWAYSOCK=$(ls "$XDG_RUNTIME_DIR"/sway-ipc.* | head -1)
# Native Wayland only: with no X server, kitty and GTK cannot fall back to X11.
unset DISPLAY
export LIBGL_ALWAYS_SOFTWARE=1

W=$OUT/$1
rm -rf "$W"; mkdir -p "$W/src" "$W/dst" "$W/root"
KOPT=(--debug-input --config NONE -o enable_audio_bell=no -o linux_display_server=wayland -o font_size=10)

# An isolated tuios. Its runtime directory is a separate, short one: the
# daemon's socket path is limited to 108 bytes, and the clients still reach
# the compositor by the absolute path of its socket.
RUN=$(mktemp -d /tmp/wt-XXXX); chmod 700 "$RUN"
TENV=(env XDG_RUNTIME_DIR=$RUN XDG_CONFIG_HOME=$W/root/config XDG_STATE_HOME=$W/root/state
      XDG_CACHE_HOME=$W/root/cache XDG_DATA_HOME=$W/root/data HOME=$W/root/home SHELL=/bin/sh
      TUIOS_DND_LOG=$W/dnd.log WAYLAND_DISPLAY=$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY)
mkdir -p "$W/root/config/tuios" "$W/root/home"
printf "[startup]\ndaemon = true\ntiled = true\nlayout = 'bsp'\n" > "$W/root/config/tuios/config.toml"
tcli() { (cd "$W" && "${TENV[@]}" "$TUIOS" "$@"); }

# rect APP_ID -> "x y w h" of the window in sway's tree.
rect() { swaymsg -t get_tree | python3 -I -c '
import json,sys
want=sys.argv[1]
def walk(n):
    if n.get("app_id")==want:
        r=n["rect"]; print(r["x"], r["y"], r["width"], r["height"]); sys.exit()
    for c in n.get("nodes",[])+n.get("floating_nodes",[]): walk(c)
walk(json.load(sys.stdin))' "$1"; }

# A virtual pointer on sway's seat: headless sway has no input device, and a
# seat without a pointer gives the windows no pointer events at all. sway's
# own `seat cursor` commands then reach nothing.
exec 7> >(exec "$XDND_VPOINTER" > "$W/vp.log" 2>&1)
sleep 0.5
vp() { echo "$*" >&7; }

# drag X1 Y1 X2 Y2 with the virtual pointer, in output pixels.
drag() {
  vp move "$1" "$2"; sleep 0.3
  vp press; sleep 0.3
  local steps=40 i
  for i in $(seq 1 $steps); do
    vp move $(( $1 + ($3-$1)*i/steps )) $(( $2 + ($4-$2)*i/steps )); sleep 0.04
  done
  sleep 0.8
  vp release
}

# panes: "ID x y w h NAME" in cells for each pane of the session.
panes() { tcli list-windows --json -s dnd | python3 -I -c '
import json,sys
for w in json.load(sys.stdin)["windows"]: print(w["window_id"], w["x"], w["y"], w["width"], w["height"], w["display_name"])'; }

# cellpx FILE NAME DX DY: the pixel of cell (DX, DY) of the pane NAME.
cellpx() { awk -v n="$2" -v dx="$3" -v dy="$4" -v bx=$BX -v by=$BY -v cw=$CW -v ch=$CH \
  '$6==n {print int(bx+($2+dx)*cw+cw/2), int(by+($3+dy)*ch+ch/2)}' "$1"; }

# yazipx FILE NAME: a cell on the first file row of yazi's middle column, the
# list it drags from, whatever the pane's width.
yazipx() { awk -v n="$2" -v bx=$BX -v by=$BY -v cw=$CW -v ch=$CH \
  '$6==n {print int(bx+($2+$4*0.4)*cw), int(by+($3+2)*ch+ch/2)}' "$1"; }

result() { local name=$1; shift; if "$@"; then echo "PASS $name"; else echo "FAIL $name"; fi; }

cleanup() {
  tcli kill-server >/dev/null 2>&1
  # kitty asks before closing a window with a program running, so the
  # windows are ended by process rather than closed.
  for p in $(swaymsg -t get_tree | grep -o '"pid": [0-9]*' | awk '{print $2}'); do kill "$p" 2>/dev/null; done
  exec 7>&-
  rm -rf "$RUN"
}
