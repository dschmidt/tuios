#!/bin/bash
# Scenario 1: a drag from another GUI program into tuios.
# Window A (left): `kitten dnd dragged.txt`, the outside source.
# Window B (right): kitty running tuios with two panes: `kitten dnd` (speaks
# OSC 72) on the left, a plain reader with bracketed paste on the right.
# Two real X11 drags: A -> left pane, A -> right pane.
source "$(dirname "$0")/lib.sh" s1
trap cleanup EXIT
echo "payload from the outside" > $W/src/dragged.txt

(cd $W/src && $K/kitty "${KOPT[@]}" --class dndA -o initial_window_width=600 -o initial_window_height=500 \
   $K/kitten dnd dragged.txt >$W/a.log 2>&1 &)
(cd $W && "${TENV[@]}" $K/kitty "${KOPT[@]}" --class tuiosB -o font_size=10 -o initial_window_width=630 -o initial_window_height=700 \
   sh -c "$TUIOS new dnd 2>$W/tuios.err; sleep 60" >$W/b.log 2>&1 &)
sleep 4
tcli new-window sink -s dnd -- sh -c "stty raw -echo; printf '\033[?2004h'; echo SINKREADY; exec cat > $W/pasted" >/dev/null
tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
# The kitten pane, made last, has the focus. Without the bridge kitty pastes
# a drop as text into the focused pane; the reader must get it only from
# the bridge, which routes by the pointer.
sleep 3
A=$(win dndA); B=$(win tuiosB)
xdotool windowmove $A 0 0; xdotool windowmove $B 640 0; sleep 1
read AX AY AW AH <<<"$(geom $A)"; read BX BY BW BH <<<"$(geom $B)"
panes > $W/panes.txt
# kitty's cell at font_size 10, as kitty reports it in each move (cell and
# pixel together). The panes do not span the window: the rail takes columns.
CW=9; CH=17
echo "A=$AX,$AY ${AW}x$AH  B=$BX,$BY ${BW}x$BH cell=${CW}x$CH"
cat $W/panes.txt
grep -q "host send t=a" $W/dnd.log && echo "tuios registered with kitty: yes" || echo "tuios registered with kitty: NO"

# The kitten draws Copy and Move zones in its middle; the source allows copy
# only, so the drop goes to the pane's upper left, where --drop-anywhere=copy
# holds.
read LX LY <<<"$(cellpx $W/panes.txt kit 4 3)"
read RX RY <<<"$(cellpx $W/panes.txt sink 10 5)"
echo "kitten pane drop point $LX,$LY   reader pane drop point $RX,$RY"

echo "== drag 1: A -> left pane (kitten dnd, speaks OSC 72)"
drag 300 250 $LX $LY; sleep 3
echo "dst: [$(ls $W/dst)]  content: $(cat $W/dst/dragged.txt 2>/dev/null)"
result "outside kitty window -> tuios pane with OSC 72 (file copied by kitten)" grep -qx "payload from the outside" $W/dst/dragged.txt

echo "== drag 2: A -> right pane (plain reader, no OSC 72)"
drag 300 250 $RX $RY; sleep 3
echo "pasted: $(cat -v $W/pasted)"
result "outside kitty window -> tuios pane without OSC 72 (bracketed paste of the path)" grep -qF "$(printf '\033[200~')$W/src/dragged.txt$(printf '\033[201~')" $W/pasted

echo "== drop and data exchange"
grep -E "host recv t=(M|r|R)|host send t=r|send [0-9a-f]+ t=(M|r|R) |recv [0-9a-f]+ t=r" $W/dnd.log | cut -c1-170
