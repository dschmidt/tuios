#!/bin/bash
# Scenario 2: a drag from a tuios pane out to another GUI program.
# Window A (left): `kitten dnd --drop-anywhere=copy`, the outside target.
# Window B (right): kitty running tuios with one pane: a source, given as $1
# ("kitten" for `kitten dnd FILE`, "yazi" for yazi on the folder).
source "$(dirname "$0")/lib.sh" s2-${1:-kitten}
trap cleanup EXIT
echo "payload from inside tuios" > $W/src/dragged.txt

(cd $W/dst && $K/kitty "${KOPT[@]}" --class dndA -o initial_window_width=600 -o initial_window_height=500 \
   $K/kitten dnd --drop-anywhere=copy >$W/a.log 2>&1 &)
(cd $W && "${TENV[@]}" $K/kitty "${KOPT[@]}" --class tuiosB -o font_size=10 -o initial_window_width=630 -o initial_window_height=700 \
   sh -c "$TUIOS new dnd 2>$W/tuios.err; sleep 60" >$W/b.log 2>&1 &)
sleep 4
if [ "${1:-kitten}" = yazi ]; then
  tcli new-window src -s dnd -- sh -c "cd $W/src && HOME=$W/root/home exec $YAZI" >/dev/null
else
  tcli new-window src -s dnd -- sh -c "cd $W/src && exec $K/kitten dnd dragged.txt" >/dev/null
fi
sleep 4
A=$(win dndA); B=$(win tuiosB)
xdotool windowmove $A 0 0; xdotool windowmove $B 640 0; sleep 1
# Terminal mode, so a press reaches the program in the pane.
terminal_mode $B
read BX BY BW BH <<<"$(geom $B)"
panes > $W/panes.txt
# kitty's cell at font_size 10, as kitty reports it in each move (cell and
# pixel together). The panes do not span the window: the rail takes columns.
CW=9; CH=17
grep -q "host send t=o" $W/dnd.log && echo "tuios offers drags to kitty: yes" || echo "tuios offers drags to kitty: NO"
# Press on the first file row for yazi (row 1 of the pane), anywhere for the kitten.
read SX SY <<<"$(yazipx $W/panes.txt src)"
echo "press at $SX,$SY"
drag $SX $SY 150 250; sleep 3
echo "dst: [$(ls $W/dst)]  content: $(cat $W/dst/dragged.txt 2>/dev/null)"
result "${1:-kitten} in a tuios pane -> outside kitty window (file copied by kitten)" grep -qx "payload from inside tuios" $W/dst/dragged.txt
echo "== exchange"
grep -E "host (send|recv) t=(o|p|P|E|e)|send [0-9a-f]+ t=(o|e|E) |recv [0-9a-f]+ t=(o|p|P|e|E) " $W/dnd.log | cut -c1-170 | head -30
