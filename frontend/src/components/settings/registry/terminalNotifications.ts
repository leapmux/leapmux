import { requestOsNotificationPermission } from '~/lib/osNotification'

/**
 * Ask the OS for notification permission, and give the value the
 * terminal-OS-notifications toggle should then store.
 *
 * Only the ON path comes here. Declining leaves the preference off rather
 * than showing it on with nothing behind it. Turning the toggle OFF stores
 * false at the toggle itself and must NOT prompt -- re-asking for a
 * permission the user is in the act of switching off is exactly the
 * prompt fatigue browsers penalize, and a denied origin answers from its
 * own sticky decision, which would flip the toggle back on.
 *
 * Split out of the component because the permission call is the part worth
 * testing and the settings row reads its state from context: reaching it
 * through a render would need the real PreferencesProvider, whose onMount
 * reloads settings over the network.
 */
export async function requestTerminalOsNotifications(): Promise<boolean> {
  return requestOsNotificationPermission()
}
