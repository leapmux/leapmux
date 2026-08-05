/**
 * Oat's toast module, which ships no types.
 *
 * The app itself reaches oat through the `window.ot` global the design system
 * installs (see `~/lib/oat`), so this declaration exists for the one caller that
 * cannot: the test that pins oat's own "a zero duration means no dismiss timer"
 * behaviour, which the sticky-toast helper is built on and which oat documents
 * nowhere.
 */
declare module '@knadh/oat/js/toast.js' {
  /** Mounts a CLONE of `el` and returns that clone, not `el` itself. */
  export function toastEl(
    el: HTMLElement,
    options?: { placement?: string, duration?: number },
  ): HTMLElement | undefined
}
