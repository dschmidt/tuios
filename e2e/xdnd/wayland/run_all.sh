#!/bin/bash
# Runs every scenario on native Wayland, under a headless sway it starts, and
# prints a summary. Exits non-zero if a check failed.
# Needs, besides what ../README.md lists: sway, gcc, wayland-scanner and the
# libwayland-client headers (libwayland-dev, libwayland-bin), and curl to
# fetch the virtual pointer protocol once.
HERE=$(cd "$(dirname "$0")" && pwd)
export XDND_OUT=${XDND_OUT:-$(mktemp -d /tmp/tuios-xdnd-wl-XXXX)}
mkdir -p "$XDND_OUT"

# The virtual pointer, built once.
VP=$XDND_OUT/vpointer
if [ ! -x "$VP" ]; then
  B=$XDND_OUT/vpbuild; mkdir -p "$B"
  curl -sSL -o "$B/vp.xml" https://gitlab.freedesktop.org/wlroots/wlr-protocols/-/raw/master/unstable/wlr-virtual-pointer-unstable-v1.xml
  wayland-scanner client-header "$B/vp.xml" "$B/vp.h"
  wayland-scanner private-code "$B/vp.xml" "$B/vp.c"
  gcc -O2 -I"$B" -o "$VP" "$HERE/vpointer.c" "$B/vp.c" -lwayland-client || exit 1
fi
export XDND_VPOINTER=$VP

# A headless sway of its own, with one 1280x800 output and no Xwayland.
export XDND_WL_RUNTIME=$(mktemp -d /tmp/xdwl-XXXX); chmod 700 "$XDND_WL_RUNTIME"
printf 'output HEADLESS-1 resolution 1280x800\nxwayland disable\ndefault_border none\n' > "$XDND_OUT/sway.conf"
XDG_RUNTIME_DIR=$XDND_WL_RUNTIME WLR_BACKENDS=headless WLR_RENDERER=pixman WLR_LIBINPUT_NO_DEVICES=1 \
  WLR_HEADLESS_OUTPUTS=1 sway -c "$XDND_OUT/sway.conf" > "$XDND_OUT/sway.log" 2>&1 &
SWAY=$!
sleep 3
export XDND_WL_DISPLAY=$(ls "$XDND_WL_RUNTIME" | grep -E '^wayland-[0-9]+$' | head -1)

for s in inbound outbound-kitten outbound-yazi same gtk; do
  bash "$HERE/scenario.sh" $s > "$XDND_OUT/$s.log" 2>&1
done
kill $SWAY; rm -rf "$XDND_WL_RUNTIME"
grep -h "^PASS\|^FAIL" "$XDND_OUT"/*.log | tee "$XDND_OUT/summary.txt"
echo "logs and the protocol exchange of each scenario: $XDND_OUT"
! grep -q "^FAIL" "$XDND_OUT/summary.txt"
