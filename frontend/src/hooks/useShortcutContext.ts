import type { Accessor } from 'solid-js'
import type { ContextValue } from '~/lib/shortcuts/types'
import { createEffect, onCleanup } from 'solid-js'
import { deleteContext, setContext } from '~/lib/shortcuts/context'

/**
 * Reactively bind a SolidJS accessor to a shortcut context key.
 * The context key is set when the value changes and deleted on cleanup.
 */
export function useShortcutContext(key: string, value: Accessor<ContextValue>): void {
  createEffect(() => {
    setContext(key, value())
  })
  onCleanup(() => deleteContext(key))
}
