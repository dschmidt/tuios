"""A GTK 3 window that takes or offers a drag of text/uri-list over X11 XDND,
standing in for a file manager or a browser.

usage: gtkdnd.py target LOG       write each dropped URI list to LOG
       gtkdnd.py source FILE...   offer FILE... as a URI list when dragged
"""
import sys
import gi
gi.require_version('Gtk', '3.0')
from gi.repository import Gtk, Gdk, GLib

mode = sys.argv[1]
# The app id a Wayland compositor knows the window by, and the class on X11.
GLib.set_prgname('gtkdnd' + mode)
win = Gtk.Window(title='gtkdnd-' + mode)
win.set_wmclass('gtkdnd' + mode, 'gtkdnd' + mode)
win.set_default_size(600, 500)
box = Gtk.EventBox()
label = Gtk.Label(label='GTK ' + mode)
box.add(label)
win.add(box)
targets = [Gtk.TargetEntry.new('text/uri-list', 0, 0)]

if mode == 'target':
    log = sys.argv[2]
    box.drag_dest_set(Gtk.DestDefaults.ALL, targets, Gdk.DragAction.COPY)

    def on_received(widget, ctx, x, y, data, info, time):
        uris = data.get_uris() or []
        with open(log, 'a') as f:
            f.write('\n'.join(uris) + '\n')
        label.set_text('got %d uri(s)' % len(uris))
    box.connect('drag-data-received', on_received)
else:
    files = sys.argv[2:]
    box.drag_source_set(Gdk.ModifierType.BUTTON1_MASK, targets, Gdk.DragAction.COPY)

    def on_get(widget, ctx, data, info, time):
        data.set_uris([GLib.filename_to_uri(f, None) for f in files])
    box.connect('drag-data-get', on_get)

win.connect('destroy', Gtk.main_quit)
win.show_all()
GLib.timeout_add_seconds(90, Gtk.main_quit)
Gtk.main()
