#!/bin/bash
# Scenario 3: pane to pane inside one tuios window, with the bridge on. kitty
# carries the drag as the window system's drag, and it comes back over the
# window it started in. kitty would refuse to serve its data (EPERM, same
# window), so tuios has to serve it from the source pane.
# Window B: kitty running tuios with yazi on top and `kitten dnd` below.
source "$(dirname "$0")/lib.sh" s3
trap cleanup EXIT
echo "payload pane to pane" > $W/src/dragged.txt

(cd $W && "${TENV[@]}" $K/kitty "${KOPT[@]}" --class tuiosB -o font_size=10 -o initial_window_width=630 -o initial_window_height=700 \
   sh -c "$TUIOS new dnd 2>$W/tuios.err; sleep 60" >$W/b.log 2>&1 &)
sleep 4
tcli new-window src -s dnd -- sh -c "cd $W/src && HOME=$W/root/home exec $YAZI" >/dev/null
tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
sleep 4
B=$(win tuiosB)
xdotool windowmove $B 640 0; sleep 1
terminal_mode $B
read BX BY BW BH <<<"$(geom $B)"
panes > $W/panes.txt
# kitty's cell at font_size 10, as kitty reports it in each move (cell and
# pixel together). The panes do not span the window: the rail takes columns.
CW=9; CH=17
# The first file row of yazi (below the border and its path header), and a
# neutral spot of the kitten pane.
read SX SY <<<"$(yazipx $W/panes.txt src)"
read DX DY <<<"$(cellpx $W/panes.txt kit 4 3)"
echo "drag $SX,$SY (yazi pane) -> $DX,$DY (kitten pane)"
drag $SX $SY $DX $DY; sleep 3
echo "dst: [$(ls $W/dst)]  content: $(cat $W/dst/dragged.txt 2>/dev/null)"
result "yazi pane -> kitten pane in the same tuios window, through the window system" grep -qx "payload pane to pane" $W/dst/dragged.txt
result "the drag ran as the window system's drag (kitty reported the drop to tuios)" grep -q "host recv t=M" $W/dnd.log
result "no data request for its own drag went to kitty" bash -c "! grep -q 'host send t=r o=0 x=1' $W/dnd.log"
echo "== exchange (no data request may reach kitty)"
grep -E "host (send|recv) t=(o|E|e|M|r|R)|t=P|send [0-9a-f]+ t=(M|r|R) |recv [0-9a-f]+ t=r " $W/dnd.log | cut -c1-170
