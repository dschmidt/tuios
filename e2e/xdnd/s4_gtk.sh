#!/bin/bash
# Scenario 4: tuios against a GTK application, both directions.
#   a) GTK source window -> tuios pane running `kitten dnd` (file copied)
#   b) yazi in a tuios pane -> GTK target window (URI list received)
source "$(dirname "$0")/lib.sh" s4
trap cleanup EXIT
echo "payload from GTK" > $W/src/from-gtk.txt
mkdir -p $W/yz; echo "payload from yazi" > $W/yz/drag-me.txt
PY=${GTK_PYTHON:-python3}

(cd $W && "${TENV[@]}" $K/kitty "${KOPT[@]}" --class tuiosB -o font_size=10 -o initial_window_width=630 -o initial_window_height=700 \
   sh -c "$TUIOS new dnd 2>$W/tuios.err; sleep 90" >$W/b.log 2>&1 &)
sleep 4
tcli new-window src -s dnd -- sh -c "cd $W/yz && HOME=$W/root/home exec $YAZI" >/dev/null
tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
sleep 4
B=$(win tuiosB); xdotool windowmove $B 640 0; sleep 1
terminal_mode $B
read BX BY BW BH <<<"$(geom $B)"
panes > $W/panes.txt
# kitty's cell at font_size 10, as kitty reports it in each move (cell and
# pixel together). The panes do not span the window: the rail takes columns.
CW=9; CH=17

echo "== a) GTK source -> kitten pane in tuios"
$PY $HERE/gtkdnd.py source $W/src/from-gtk.txt >$W/gtk-src.log 2>&1 &
sleep 3; G=$(win gtkdndsource); xdotool windowmove $G 0 0; sleep 1
read DX DY <<<"$(cellpx $W/panes.txt kit 4 3)"
drag 300 250 $DX $DY; sleep 3
echo "dst: [$(ls $W/dst)]  content: $(cat $W/dst/from-gtk.txt 2>/dev/null)"
result "GTK window -> tuios pane with OSC 72 (file copied by kitten)" grep -qx "payload from GTK" $W/dst/from-gtk.txt
xdotool search --class gtkdndsource windowkill 2>/dev/null; sleep 1

echo "== b) yazi pane in tuios -> GTK target"
$PY $HERE/gtkdnd.py target $W/gtk-got.txt >$W/gtk-dst.log 2>&1 &
sleep 3; G=$(win gtkdndtarget); xdotool windowmove $G 0 0; sleep 1
read SX SY <<<"$(yazipx $W/panes.txt src)"
drag $SX $SY 300 250; sleep 3
echo "GTK received: $(cat $W/gtk-got.txt 2>/dev/null)"
result "yazi in a tuios pane -> GTK window (URI list received)" grep -qx "file://$W/yz/drag-me.txt" $W/gtk-got.txt

echo "== exchange"
grep -E "host (send|recv) t=(o|E|M|r|R|e)|host send t=P" $W/dnd.log | cut -c1-170
