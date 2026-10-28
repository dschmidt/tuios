// vpointer: a virtual pointer for a wlroots compositor, driven from stdin.
// Lines: "move X Y" (absolute, in output pixels), "press", "release".
// It keeps the device for as long as it runs, so the seat has a pointer.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <wayland-client.h>
#include "vp.h"

#define BTN_LEFT 0x110

static struct zwlr_virtual_pointer_manager_v1 *mgr;
static struct wl_seat *seat;

static void reg_global(void *d, struct wl_registry *r, uint32_t name, const char *iface, uint32_t ver) {
    if (strcmp(iface, zwlr_virtual_pointer_manager_v1_interface.name) == 0)
        mgr = wl_registry_bind(r, name, &zwlr_virtual_pointer_manager_v1_interface, 1);
    else if (strcmp(iface, wl_seat_interface.name) == 0 && !seat)
        seat = wl_registry_bind(r, name, &wl_seat_interface, 1);
}
static void reg_remove(void *d, struct wl_registry *r, uint32_t name) {}
static const struct wl_registry_listener reg = {reg_global, reg_remove};

static uint32_t now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint32_t)(ts.tv_sec * 1000 + ts.tv_nsec / 1000000);
}

int main(int argc, char **argv) {
    uint32_t w = argc > 1 ? atoi(argv[1]) : 1280, h = argc > 2 ? atoi(argv[2]) : 800;
    struct wl_display *dpy = wl_display_connect(NULL);
    if (!dpy) { fprintf(stderr, "no wayland display\n"); return 1; }
    struct wl_registry *r = wl_display_get_registry(dpy);
    wl_registry_add_listener(r, &reg, NULL);
    wl_display_roundtrip(dpy);
    if (!mgr) { fprintf(stderr, "no zwlr_virtual_pointer_manager_v1\n"); return 1; }
    struct zwlr_virtual_pointer_v1 *p = zwlr_virtual_pointer_manager_v1_create_virtual_pointer(mgr, seat);
    wl_display_roundtrip(dpy);
    printf("ready\n");
    fflush(stdout);
    char line[128];
    while (fgets(line, sizeof line, stdin)) {
        int x, y;
        if (sscanf(line, "move %d %d", &x, &y) == 2) {
            zwlr_virtual_pointer_v1_motion_absolute(p, now_ms(), x, y, w, h);
        } else if (strncmp(line, "press", 5) == 0) {
            zwlr_virtual_pointer_v1_button(p, now_ms(), BTN_LEFT, WL_POINTER_BUTTON_STATE_PRESSED);
        } else if (strncmp(line, "release", 7) == 0) {
            zwlr_virtual_pointer_v1_button(p, now_ms(), BTN_LEFT, WL_POINTER_BUTTON_STATE_RELEASED);
        } else {
            continue;
        }
        zwlr_virtual_pointer_v1_frame(p);
        wl_display_flush(dpy);
        wl_display_dispatch_pending(dpy);
    }
    zwlr_virtual_pointer_v1_destroy(p);
    wl_display_roundtrip(dpy);
    return 0;
}
