//go:build linux

package main

/*
#cgo linux pkg-config: gtk+-3.0

#include <gtk/gtk.h>

// Intercept Tab/Shift+Tab at the GTK level to prevent focus traversal.
//
// In GTK3, the GtkWindow's default key-press-event handler first propagates
// the event to the focused widget (WebKitWebView), then does focus traversal
// for Tab if the event wasn't consumed. WebKitGTK returns FALSE for Tab,
// so GTK moves focus away from the WebView.
//
// This handler runs before the default handler (G_SIGNAL_RUN_LAST).
// It manually propagates Tab to the focused widget so WebKitGTK generates
// the JS keydown event for ProseMirror, then returns TRUE to suppress
// GTK's focus traversal.
static gboolean on_tab_key_press(GtkWidget *widget, GdkEventKey *event, gpointer data) {
	if (event->keyval == GDK_KEY_Tab || event->keyval == GDK_KEY_ISO_Left_Tab) {
		gtk_window_propagate_key_event(GTK_WINDOW(widget), event);
		return TRUE;
	}
	return FALSE;
}

static int tab_handler_installed = 0;

static gboolean install_tab_handler_idle(gpointer data) {
	if (tab_handler_installed) return G_SOURCE_REMOVE;
	tab_handler_installed = 1;

	GList *toplevels = gtk_window_list_toplevels();
	for (GList *l = toplevels; l != NULL; l = l->next) {
		if (GTK_IS_WINDOW(l->data)) {
			g_signal_connect(l->data, "key-press-event",
				G_CALLBACK(on_tab_key_press), NULL);
		}
	}
	g_list_free(toplevels);
	return G_SOURCE_REMOVE;
}

void doInstallTabKeyHandler() {
	g_idle_add(install_tab_handler_idle, NULL);
}
*/
import "C"

func installTabKeyHandler() {
	C.doInstallTabKeyHandler()
}
