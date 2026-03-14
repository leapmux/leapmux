//go:build linux

package main

/*
#cgo linux pkg-config: gtk+-3.0 webkit2gtk-4.1

#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

// Intercept Tab/Shift+Tab at the GTK level to prevent focus traversal,
// then inject a synthetic JS KeyboardEvent so ProseMirror can handle it.
//
// We cannot use gtk_window_propagate_key_event() because it walks up the
// widget hierarchy, and each parent's GtkWidget base handler checks key
// bindings — the Tab binding triggers move-focus, causing focus traversal
// despite our handler returning TRUE.
//
// Instead, we suppress the GTK event entirely and dispatch a synthetic
// keydown event to document.activeElement via JS. If ProseMirror is
// focused, it handles Tab (heading convert, list indent, plan mode toggle,
// etc.). If not, we manually move focus to the next/previous focusable
// HTML element.
static gboolean on_tab_key_press(GtkWidget *widget, GdkEventKey *event, gpointer data) {
	if (event->keyval != GDK_KEY_Tab && event->keyval != GDK_KEY_ISO_Left_Tab)
		return FALSE;

	GtkWidget *focus = gtk_window_get_focus(GTK_WINDOW(widget));
	if (!focus || !WEBKIT_IS_WEB_VIEW(focus))
		return FALSE;

	gboolean shift = (event->state & GDK_SHIFT_MASK) != 0;

	const char *js = shift ?
		"(function(){"
		"var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:true,bubbles:true,cancelable:true});"
		"if(document.activeElement.dispatchEvent(e)){"
		"var f=Array.from(document.querySelectorAll("
		"'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),"
		"textarea:not([disabled]),[tabindex]:not([tabindex=\"-1\"]),[contenteditable]'"
		")).filter(function(x){return x.offsetParent!==null});"
		"var i=f.indexOf(document.activeElement);"
		"if(i>0)f[i-1].focus();"
		"}"
		"})()"
		:
		"(function(){"
		"var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:false,bubbles:true,cancelable:true});"
		"if(document.activeElement.dispatchEvent(e)){"
		"var f=Array.from(document.querySelectorAll("
		"'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),"
		"textarea:not([disabled]),[tabindex]:not([tabindex=\"-1\"]),[contenteditable]'"
		")).filter(function(x){return x.offsetParent!==null});"
		"var i=f.indexOf(document.activeElement);"
		"if(i>=0&&i<f.length-1)f[i+1].focus();"
		"}"
		"})()";

	webkit_web_view_evaluate_javascript(WEBKIT_WEB_VIEW(focus), js, -1, NULL, NULL, NULL, NULL, NULL);
	return TRUE;
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
