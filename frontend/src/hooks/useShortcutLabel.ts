import type { Accessor } from 'solid-js'
import { formatShortcut } from '~/lib/shortcuts/display'
import { getBindingForCommand } from '~/lib/shortcuts/keybindings'
import { getPlatform } from '~/lib/shortcuts/platform'

/**
 * Returns a reactive accessor that provides the formatted shortcut label
 * for the given command ID (e.g. '⌘N' on Mac, 'Ctrl+N' on Windows).
 *
 * Returns undefined if the command has no keybinding.
 */
export function useShortcutLabel(commandId: string): Accessor<string | undefined> {
  const platform = getPlatform()
  return () => {
    const key = getBindingForCommand(commandId)
    if (!key)
      return undefined
    return formatShortcut(key, platform)
  }
}
