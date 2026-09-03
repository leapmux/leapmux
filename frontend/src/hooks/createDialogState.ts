import type { Accessor } from 'solid-js'
import { createSignal } from 'solid-js'

/**
 * Imperative handle for a dialog that carries a payload while open. `open`
 * stores the payload; `close` resets to null; `value` is the reactive
 * payload (null when closed — `<Show when={state.value()} keyed>` is the
 * typical consumer pattern).
 *
 * The shape collapses the show-flag + payload + setter triple that parents
 * used to thread through props. Use {@link UpdatableDialogState} for a dialog
 * whose payload is patched in place.
 *
 * There is deliberately NO `update` here. Whether a payload can change
 * while the dialog stays open decides how the consumer renders it: a
 * payload that only ever arrives through `open` can be rendered under a
 * keyed `<Show>`, which hands the children the payload itself and so has no
 * accessor for a post-close callback to read. An in-place `update` breaks
 * that, because keyed re-creates the subtree on every payload identity
 * change and would remount the native `<dialog>` on each refresh. Splitting
 * the two capabilities across two types puts that rule in the type system
 * rather than in a comment, so a dialog cannot silently gain `update` and
 * invalidate its parent's render form.
 */
export interface DialogState<T> {
  open: (value: T) => void
  close: () => void
  value: Accessor<T | null>
}

/**
 * A {@link DialogState} whose payload can also be patched in place, for a
 * dialog that refreshes its own contents while the user is staring at them
 * (re-running an inspect RPC, for example).
 *
 * Render one of these under a NON-keyed `<Show>`: keyed would tear the
 * dialog down and rebuild it on every patch. That is the whole reason this
 * is a separate type — see {@link DialogState}.
 */
export interface UpdatableDialogState<T> extends DialogState<T> {
  /**
   * Merge `patch` into the current payload. No-op when the dialog is
   * closed (the caller should `open` first). Returns whether a write
   * was performed.
   */
  update: (patch: Partial<T>) => boolean
}

export function createUpdatableDialogState<T>(): UpdatableDialogState<T> {
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

/**
 * The open/close-only handle. Built on the updatable one and narrowed by the
 * return type, so a caller cannot reach `update` — which is the point: see
 * {@link DialogState} for why that capability decides the render form.
 */
export function createDialogState<T>(): DialogState<T> {
  return createUpdatableDialogState<T>()
}
