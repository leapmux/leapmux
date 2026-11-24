import type { NotificationToastContent } from '~/components/common/Toast'
import { showNotificationToast } from '~/components/common/Toast'
import { showDesktopNotification } from '~/lib/osNotification'

/** A notification that a process asked for through `leapmux control notify`. */
export interface UserNotificationContent extends NotificationToastContent {
  id: string
}

/**
 * Show a notification that a process asked for: a toast in this window always,
 * and a desktop notification as well while the page is hidden, when the user
 * turned desktop notifications on (see `showDesktopNotification`).
 *
 * Every open window of the account shows the toast, because each one holds
 * its own stream. The desktop notification uses the Worker's ID so each
 * window uses the same tag for the same post.
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
  const tag = `leapmux-notify:${notification.id}`
  showDesktopNotification({ title: notification.title, body, tag }, () => {})
}
