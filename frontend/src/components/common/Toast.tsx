import { isDisconnectError } from '~/api/workerErrors'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'

const log = createLogger('toast')

type ToastType = 'danger' | 'success'

/** Show a warning toast and log the error at warn level. */
export function showWarnToast(message: string, err?: unknown) {
  log.warn(message, err)
  renderToast(formatErrorMessage(err, message), 'danger')
}

/**
 * Show a warning toast whose copy the CALLER owns, and log `err` as the cause.
 *
 * `showWarnToast` renders `err.message` and keeps its own `message` only as a
 * fallback for a thrown non-Error. That is right when the error carries a
 * sentence the user can act on, and wrong for a transport failure, whose message
 * names our own plumbing: "channel not open", "channel disconnected", "cannot
 * send channel message: WebSocket not open". The user read the jargon and the
 * app's real sentence never reached the screen.
 *
 * Use this where the caller knows what to say and the error is a diagnostic.
 */
export function showWarnToastWithLoggedCause(message: string, err: unknown) {
  log.warn(message, err)
  renderToast(message, 'danger')
}

/**
 * Show a warning toast, unless a dropped connection is what failed the
 * operation.
 *
 * For a BACKGROUND operation the app retries on its own. See `isDisconnectError`
 * for which failures qualify and why a user-requested operation must not use
 * this.
 *
 * It lives here, beside its siblings, rather than in a module of its own: a
 * reader choosing a toast helper then sees every option in one place, and the
 * import it costs is one predicate with no cycle back to the components layer.
 */
export function showWarnToastUnlessDisconnected(message: string, err: unknown) {
  if (isDisconnectError(err)) {
    log.debug('suppressed a background failure that the dropped connection explains', { message, err })
    return
  }
  showWarnToast(message, err)
}

/**
 * Show a warning toast that stays until the user dismisses it.
 *
 * For states that do NOT recover on their own — a user-events stream closed
 * with a terminal code, say, where nothing schedules a retry. The default
 * three-second toast is sized for "something went wrong, we are handling it";
 * a user who looks away for four seconds would otherwise find a frozen UI with
 * the only explanation already gone.
 */
export function showStickyWarnToast(message: string, err?: unknown) {
  log.warn(message, err)
  renderToast(formatErrorMessage(err, message), 'danger', 0)
}

/** Show an error toast and log the error at error level. */
export function showErrorToast(message: string, err?: unknown) {
  log.error(message, err)
  renderToast(formatErrorMessage(err, message), 'danger')
}

/** Show an informational (success) toast. */
export function showInfoToast(message: string) {
  renderToast(message, 'success')
}

/**
 * Live sticky toasts, keyed by the text they render.
 *
 * A sticky toast has no dismiss timer, so the only thing that ends one is the
 * user's click — which is why "announce this once" cannot live in a CALLER. A
 * caller-side latch never learns about the dismissal, so it turns one click into
 * a permanent mute and the next genuinely-new refusal is announced nowhere at
 * all. Here the entry dies with the toast, so the suppression lasts exactly as
 * long as the message is still on screen.
 *
 * Keyed on the rendered text, because that is what the user would be shown
 * twice: two causes that render the same sentence have nothing to add by
 * stacking, and two that render differently are not deduped.
 */
const liveSticky = new Map<string, HTMLElement>()

// durationMs of 0 means "until dismissed"; the toast already renders its own
// close button, so a sticky one is never a dead end.
function renderToast(message: string, type: ToastType, durationMs = 3000) {
  const sticky = durationMs === 0
  if (sticky) {
    // isConnected rather than mere presence: the map is a cache over the DOM,
    // never a second source of truth, so a toast removed by any other path
    // simply stops suppressing instead of muting the message for good.
    const live = liveSticky.get(message)
    if (live?.isConnected)
      return
    liveSticky.delete(message)
  }
  const variant = type === 'success' ? 'success' : 'danger'

  const toast = document.createElement('output')
  toast.setAttribute('data-variant', variant)
  toast.style.display = 'flex'
  toast.style.alignItems = 'start'
  toast.style.gap = 'var(--space-3)'

  const msgEl = document.createElement('div')
  msgEl.className = 'toast-message'
  msgEl.style.flex = '1'
  msgEl.textContent = message
  toast.appendChild(msgEl)

  const closeBtn = document.createElement('button')
  closeBtn.setAttribute('data-close', '')
  closeBtn.textContent = '\u00D7'
  toast.appendChild(closeBtn)

  // Oat's toastEl() mounts `el.cloneNode(true)`, never the node it was handed,
  // and cloneNode copies attributes but not property-assigned handlers. So a
  // handler wired here, before the call, is dropped from the copy the user
  // actually sees. Auto-dismissal hid that for as long as every toast expired
  // on its own; a sticky toast makes the button the only way out. _show()
  // returns the mounted clone, so bind to that.
  const mounted = window.ot.toast.el(toast, {
    placement: 'bottom-right',
    duration: durationMs,
  })
  if (!mounted) {
    return
  }

  if (sticky) {
    liveSticky.set(message, mounted)
  }

  mounted.querySelector('[data-close]')?.addEventListener('click', () => {
    // Released on the CLICK, not when the exit transition finishes: the user has
    // said they are done with this message, so a refusal arriving mid-fade is a
    // new one and must be shown.
    if (liveSticky.get(message) === mounted) {
      liveSticky.delete(message)
    }
    dismissToast(mounted)
  })
}

/**
 * Take a toast out the way oat's own dismiss timer would.
 *
 * oat's `_remove` is module-private, so this repeats it: mark the toast
 * exiting, let the transition play, then drop it and hide the shared container
 * once it holds nothing. A bare `remove()` also cleared the screen -- an empty
 * container is invisible either way, being pointer-events: none, transparent
 * and backdrop-less -- but it skipped the fade every other toast gets and left
 * a `popover="manual"` element in the top layer with zero children until some
 * later toast's timer happened to tidy up. A sticky toast has no timer of its
 * own, so the one toast a user MUST dismiss by hand was the only one that left
 * on a different path from all the others.
 */
function dismissToast(el: HTMLElement) {
  if (el.hasAttribute('data-exiting')) {
    return
  }
  el.setAttribute('data-exiting', '')

  const container = el.parentElement
  const cleanup = () => {
    el.remove()
    if (container && container.children.length === 0) {
      container.hidePopover()
    }
  }
  el.addEventListener('transitionend', cleanup, { once: true })

  // transitionend is a hint, not a promise -- a client with animations disabled
  // never fires it -- so the declared duration is also a deadline. Read from
  // the same custom property oat reads, in whichever unit it was written.
  const declared = getComputedStyle(el).getPropertyValue('--transition').trim()
  const value = Number.parseFloat(declared)
  setTimeout(cleanup, Number.isNaN(value) ? 0 : declared.endsWith('ms') ? value : value * 1000)
}
