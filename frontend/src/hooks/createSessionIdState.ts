import type { Accessor } from 'solid-js'
import { createMemo, createSignal } from 'solid-js'
import { validateSessionId } from '~/lib/validate'

export interface SessionIdState {
  /** Current value (raw, untrimmed). */
  value: Accessor<string>
  setValue: (v: string) => void
  /** Validation error for the current value, or null when empty / valid. */
  error: Accessor<string | null>
  /** Trimmed value — empty string means "no session id". */
  trimmed: Accessor<string>
}

/**
 * Reactive state for an optional "resume an existing session" input.
 * The trimmed value collapses leading/trailing whitespace away so callers
 * can use `state.trimmed()` directly as the wire payload.
 */
export function createSessionIdState(): SessionIdState {
  const [value, setValue] = createSignal('')
  const trimmed = createMemo(() => value().trim())
  const error = createMemo(() => {
    const v = trimmed()
    if (!v)
      return null
    return validateSessionId(v)
  })
  return { value, setValue, error, trimmed }
}
