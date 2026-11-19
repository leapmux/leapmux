import { showNotificationToast } from '~/components/common/Toast'
import { showDesktopNotification } from '~/lib/osNotification'

/** A notification that a process asked for through `leapmux control notify`. */
export interface UserNotificationContent {
  title: string
  /** More text below the title. Empty shows none. */
  body: string
  /** The title of the tab that sent it, when this window knows the tab. */
  sourceTabTitle: string | undefined
}

/**
 * Show a notification that a process asked for: a toast in this window always,
 * and a desktop notification as well while the page is hidden, when the user
 * turned desktop notifications on (see `showDesktopNotification`).
 *
 * Every open window of the account shows the toast, because each one holds
 * its own stream. The desktop notification carries a tag built from its text,
 * so the browser shows one notification, not one per hidden window. The tag
 * encodes the title and the body as a JSON array: a title can hold a line
 * break, so a tag that joined the two with one would give two different
 * notifications one tag, and the browser would replace the first.
 */
export function showUserNotification(notification: UserNotificationContent): void {
  showNotificationToast(notification)
  if (document.visibilityState !== 'hidden')
    return
  const lines = [notification.body]
  if (notification.sourceTabTitle)
    lines.push(`From ${notification.sourceTabTitle}`)
  const body = lines.filter(line => line !== '').join('\n')
  // The toast above already told the user, so a missing desktop notification
  // needs no fallback.
  const tag = `leapmux-notify:${JSON.stringify([notification.title, body])}`
  showDesktopNotification({ title: notification.title, body, tag }, () => {})
}
