import type { Accessor } from 'solid-js'
import { createSignal } from 'solid-js'

/**
 * Imperative handle for a dialog that carries a payload while open. `open`
 * stores the payload; `close` resets to null; `value` is the reactive
 * payload (null when closed — `<Show when={state.value()}>` is the typical
 * consumer pattern). `update` merges a partial patch into the current
 * payload without going through close/reopen — keeps the dialog's
 * lifecycle stable for in-place refreshes (e.g. re-running an inspect
 * RPC while the user is staring at the result).
 *
 * The shape collapses the show-flag + payload + setter triple that parents
 * used to thread through props. Use {@link ToggleDialogState} for
 * payload-less dialogs.
 */
export interface DialogState<T> {
  open: (value: T) => void
  close: () => void
  /**
   * Merge `patch` into the current payload. No-op when the dialog is
   * closed (the caller should `open` first). Returns whether a write
   * was performed.
   */
  update: (patch: Partial<T>) => boolean
  value: Accessor<T | null>
}

/**
 * Imperative handle for a payload-less dialog (a pure "shown / hidden"
 * toggle). `isOpen` is a boolean accessor; consumers gate with
 * `<Show when={state.isOpen()}>`.
 */
export interface ToggleDialogState {
  open: () => void
  close: () => void
  isOpen: Accessor<boolean>
}

export function createDialogState<T>(): DialogState<T> {
  const [value, setValue] = createSignal<T | null>(null)
  return {
    open: v => setValue(() => v),
    close: () => setValue(null),
    update: (patch) => {
      const current = value()
      if (current === null)
        return false
      setValue(() => ({ ...current, ...patch }))
      return true
    },
    value,
  }
}

export function createToggleDialog(): ToggleDialogState {
  const [isOpen, setIsOpen] = createSignal(false)
  return {
    open: () => setIsOpen(true),
    close: () => setIsOpen(false),
    isOpen,
  }
}
