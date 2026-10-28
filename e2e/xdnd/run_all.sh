#!/bin/bash
# Runs every scenario and prints a summary. Exits non-zero if one failed.
# Needs: Xvfb, xdotool, kitty >= 0.49 (KITTY_BIN), yazi (YAZI_BIN), a tuios
# binary (TUIOS_BIN), and GTK 3 bindings for GTK_PYTHON (python3 by default).
HERE=$(cd "$(dirname "$0")" && pwd)
export XDND_OUT=${XDND_OUT:-$(mktemp -d /tmp/tuios-xdnd-XXXX)}
mkdir -p "$XDND_OUT"
for s in "s1_inbound.sh" "s2_outbound.sh kitten" "s2_outbound.sh yazi" "s3_same_window.sh" "s4_gtk.sh"; do
  bash $HERE/$s > "$XDND_OUT/$(echo $s | tr ' ' '-').log" 2>&1
done
grep -h "^PASS\|^FAIL" "$XDND_OUT"/*.log | tee "$XDND_OUT/summary.txt"
echo "logs and the protocol exchange of each scenario: $XDND_OUT"
! grep -q "^FAIL" "$XDND_OUT/summary.txt"
