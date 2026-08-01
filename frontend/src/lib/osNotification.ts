import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { showInfoToast } from '~/components/common/Toast'
import { loadBrowserPrefs } from '~/lib/browserStorage'

/** Dedupe window for the same notification tag (ms). */
const TAG_DEDUPE_MS = 3_000
/**
 * Hard cap on the dedupe set. The only production tag today is the terminalId
 * (low cardinality), so reaching the cap needs 256+ distinct notifying
 * terminals inside a 3s window. At the cap, drop everything rather than
 * refusing to dedupe — dedupe is best-effort, not a guarantee.
 */
const MAX_RECENT_TAGS = 256

const recentTags = new Map<string, number>()

function pruneExpiredTags(now: number): void {
  for (const [tag, at] of recentTags) {
    if (now - at >= TAG_DEDUPE_MS)
      recentTags.delete(tag)
  }
  if (recentTags.size > MAX_RECENT_TAGS) {
    // Unreachable in normal use (the 3s window expires entries faster than 256
    // distinct tags accumulate), but bounds memory against a pathological burst.
    recentTags.clear()
  }
}

/** Whether OS notifications can be shown (API present; permission may still be needed). */
export function osNotificationsAvailable(): boolean {
  if (isTauriApp())
    return true
  return typeof Notification !== 'undefined'
}

/** Request permission; returns true when granted. */
export async function requestOsNotificationPermission(): Promise<boolean> {
  if (isTauriApp())
    return platformBridge.requestNotificationPermission()
  if (typeof Notification === 'undefined')
    return false
  if (Notification.permission === 'granted')
    return true
  if (Notification.permission === 'denied')
    return false
  const result = await Notification.requestPermission()
  return result === 'granted'
}

function prefEnabled(): boolean {
  return loadBrowserPrefs().terminalOsNotifications === true
}

/**
 * Best-effort desktop/browser notification. Falls back to an in-app toast when
 * unavailable, denied, or the user has not opted in. The tag dedupe applies to
 * BOTH paths so a chatty program (e.g. a build loop emitting OSC 9 every loop)
 * cannot spam toasts when OS notifications are disabled — without it the
 * pre-pref `return` skips the dedupe entirely.
 */
export function notifyOs(opts: { title: string, body: string, tag?: string }): void {
  const message = opts.body ? `${opts.title}: ${opts.body}` : opts.title
  const now = Date.now()
  if (opts.tag) {
    pruneExpiredTags(now)
    const last = recentTags.get(opts.tag)
    if (last !== undefined && now - last < TAG_DEDUPE_MS) {
      // Within the dedupe window — OS replace-by-tag already covers this, and
      // a toast would spam; suppress both. After the window, notify again.
      return
    }
    recentTags.set(opts.tag, now)
  }

  if (!prefEnabled()) {
    showInfoToast(message)
    return
  }

  if (isTauriApp()) {
    void platformBridge.showNotification(opts.title, opts.body, opts.tag).catch(() => {
      showInfoToast(message)
    })
    return
  }
  if (!osNotificationsAvailable() || Notification.permission !== 'granted') {
    showInfoToast(message)
    return
  }
  try {
    const notification = new Notification(opts.title, { body: opts.body, tag: opts.tag })
    void notification
  }
  catch {
    showInfoToast(message)
  }
}

/** Test helper: clear dedupe set. */
export function _resetOsNotificationDedupeForTests(): void {
  recentTags.clear()
}
