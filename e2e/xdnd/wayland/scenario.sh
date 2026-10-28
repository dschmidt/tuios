#!/bin/bash
# The tuios drag and drop scenarios on native Wayland (headless sway).
# Usage, through run_all.sh: scenario.sh SCENARIO   (inbound | outbound-kitten | outbound-yazi | same | gtk)
source "$(dirname "$0")/lib.sh" "$1"
trap cleanup EXIT
GTKPY=${GTK_PYTHON:-python3}
GTK=$HERE/../gtkdnd.py

# The session and its panes are made first, detached, and the client in
# kitty attaches to them: an attached client starts in terminal mode, where a
# press reaches the program in the pane. Pressing i to get there raced the
# window's focus.
start_session() { tcli new dnd --detach >/dev/null; }
attach_client() {
  (cd $W && "${TENV[@]}" $K/kitty "${KOPT[@]}" --class tuiosB sh -c "$TUIOS attach dnd 2>$W/tuios.err; sleep 90" >$W/b.log 2>&1 &)
  sleep 4
}
# geometry: the window of tuios, its cell size and its panes, read now.
geometry() {
  sleep 1.5
  read BX BY BW BH <<<"$(rect tuiosB)"
  panes > $W/panes.txt
  local cols rows
  cols=$(awk '{if ($2+$4>m) m=$2+$4} END {print m}' $W/panes.txt)
  rows=$(awk '{if ($3+$5>m) m=$3+$5} END {print m}' $W/panes.txt)
  # kitty's cell at font_size 10, as kitty itself reports it: every move it
  # sends carries the cell and the pixel. The panes do not span the window,
  # since the session rail takes columns, so the window's width divided by
  # the panes' columns is not the cell.
  CW=9; CH=17
  echo "tuios window $BX,$BY ${BW}x$BH cell ${CW}x$CH"; cat $W/panes.txt
}
case $1 in
inbound)
  echo "payload from the outside" > $W/src/dragged.txt
  (cd $W/src && $K/kitty "${KOPT[@]}" --class dndA $K/kitten dnd dragged.txt >$W/a.log 2>&1 &)
  sleep 3
  start_session
  tcli new-window sink -s dnd -- sh -c "stty raw -echo; printf '\033[?2004h'; echo SINKREADY; exec cat > $W/pasted" >/dev/null
  tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
  # The kitten pane, made last, has the focus. Without the bridge kitty pastes
  # a drop as text into the focused pane; the reader must get it only from
  # the bridge, which routes by the pointer.
  attach_client
  geometry
  read AX AY AW AH <<<"$(rect dndA)"
  read LX LY <<<"$(cellpx $W/panes.txt kit 4 3)"; read RX RY <<<"$(cellpx $W/panes.txt sink 10 5)"
  drag $((AX+AW/2)) $((AY+AH/3)) $LX $LY; sleep 3
  result "wayland: outside kitty window -> tuios pane with OSC 72 (file copied by kitten)" grep -qx "payload from the outside" $W/dst/dragged.txt
  drag $((AX+AW/2)) $((AY+AH/3)) $RX $RY; sleep 3
  result "wayland: outside kitty window -> tuios pane without OSC 72 (bracketed paste of the path)" grep -qF "$(printf '\033[200~')$W/src/dragged.txt$(printf '\033[201~')" $W/pasted
  ;;
outbound-kitten|outbound-yazi)
  echo "payload from inside tuios" > $W/src/dragged.txt
  (cd $W/dst && $K/kitty "${KOPT[@]}" --class dndA $K/kitten dnd --drop-anywhere=copy >$W/a.log 2>&1 &)
  sleep 3
  start_session
  if [ $1 = outbound-yazi ]; then
    tcli new-window src -s dnd -- sh -c "cd $W/src && HOME=$W/root/home exec $YAZI" >/dev/null
  else
    tcli new-window src -s dnd -- sh -c "cd $W/src && exec $K/kitten dnd dragged.txt" >/dev/null
  fi
  attach_client
  sleep 3; geometry
  read AX AY AW AH <<<"$(rect dndA)"
  read SX SY <<<"$(yazipx $W/panes.txt src)"
  drag $SX $SY $((AX+AW/4)) $((AY+AH/4)); sleep 3
  result "wayland: ${1#outbound-} in a tuios pane -> outside kitty window (file copied by kitten)" grep -qx "payload from inside tuios" $W/dst/dragged.txt
  ;;
same)
  echo "payload pane to pane" > $W/src/dragged.txt
  start_session
  tcli new-window src -s dnd -- sh -c "cd $W/src && HOME=$W/root/home exec $YAZI" >/dev/null
  tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
  attach_client
  sleep 3; geometry
  read SX SY <<<"$(yazipx $W/panes.txt src)"; read DX DY <<<"$(cellpx $W/panes.txt kit 4 3)"
  drag $SX $SY $DX $DY; sleep 3
  result "wayland: yazi pane -> kitten pane in the same tuios window (file copied)" grep -qx "payload pane to pane" $W/dst/dragged.txt
  result "wayland: the drag ran as the compositor's drag (kitty reported the drop)" grep -q "host recv t=M" $W/dnd.log
  result "wayland: no data request for its own drag went to kitty" bash -c "! grep -q 'host send t=r o=0 x=1' $W/dnd.log"
  ;;
gtk)
  echo "payload from GTK" > $W/src/from-gtk.txt
  mkdir -p $W/yz; echo "payload from yazi" > $W/yz/drag-me.txt
  start_session
  tcli new-window src -s dnd -- sh -c "cd $W/yz && HOME=$W/root/home exec $YAZI" >/dev/null
  tcli new-window kit -s dnd -- sh -c "cd $W/dst && exec $K/kitten dnd --drop-anywhere=copy" >/dev/null
  GDK_BACKEND=wayland $GTKPY $GTK source $W/src/from-gtk.txt >$W/gtk-src.log 2>&1 &
  attach_client
  sleep 3; geometry
  read GX GY GW GH <<<"$(rect gtkdndsource)"; echo "gtk source $GX,$GY ${GW}x$GH"
  read DX DY <<<"$(cellpx $W/panes.txt kit 4 3)"
  drag $((GX+GW/2)) $((GY+GH/2)) $DX $DY; sleep 3
  result "wayland: GTK window -> tuios pane with OSC 72 (file copied by kitten)" grep -qx "payload from GTK" $W/dst/from-gtk.txt
  kill %1 2>/dev/null; sleep 1
  GDK_BACKEND=wayland $GTKPY $GTK target $W/gtk-got.txt >$W/gtk-dst.log 2>&1 &
  sleep 3; geometry
  read GX GY GW GH <<<"$(rect gtkdndtarget)"; echo "gtk target $GX,$GY ${GW}x$GH"
  read SX SY <<<"$(yazipx $W/panes.txt src)"
  drag $SX $SY $((GX+GW/2)) $((GY+GH/2)); sleep 3
  result "wayland: yazi in a tuios pane -> GTK window (URI list received)" grep -qx "file://$W/yz/drag-me.txt" $W/gtk-got.txt
  kill %2 2>/dev/null
  ;;
esac
