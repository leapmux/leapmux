export interface SettingsChange {
  permissionMode?: { old: string, new: string }
}

type Predicate = (changes: SettingsChange) => boolean

interface Listener {
  predicate: Predicate
  resolve: () => void
}

let listeners: Listener[] = []

/**
 * Called by useWorkspaceConnection when a settings_changed LEAPMUX
 * message is received. Resolves any waiting promises whose predicate matches.
 */
export function emitSettingsChanged(changes: SettingsChange): void {
  const matched: Listener[] = []
  const remaining: Listener[] = []
  for (const entry of listeners) {
    if (entry.predicate(changes)) {
      matched.push(entry)
    }
    else {
      remaining.push(entry)
    }
  }
  listeners = remaining
  for (const entry of matched) {
    entry.resolve()
  }
}

/**
 * Returns a promise that resolves when a settings_changed event matching the
 * predicate arrives, or rejects on timeout.
 */
export function waitForSettingsChanged(
  predicate: Predicate,
  timeoutMs: number,
): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout> | undefined

    const entry: Listener = {
      predicate,
      resolve: () => {
        clearTimeout(timer)
        resolve()
      },
    }

    listeners.push(entry)

    timer = setTimeout(() => {
      const idx = listeners.indexOf(entry)
      if (idx !== -1) {
        listeners.splice(idx, 1)
      }
      reject(new Error('Timed out waiting for settings_changed'))
    }, timeoutMs)
  })
}

/** Visible for testing: clear all pending listeners. */
export function _resetListeners(): void {
  listeners = []
}
