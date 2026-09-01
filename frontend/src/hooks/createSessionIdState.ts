import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMemo, createSignal } from 'solid-js'
import { pluginFor } from '~/components/chat/providers/registry'
import { validateSessionFileHandle, validateSessionId } from '~/lib/validate'

// Side-effect import: `pluginFor` answers undefined for a provider whose module
// never loaded, and undefined here reads as "this handle is a token" -- the
// exact rule that refused every Pi session path. The dialogs that own this
// field reach the registry through `openAgentRequestOptions` only, which
// registers nothing, so this module imports the plugins it asks about rather
// than depending on another module having loaded them first.
import '~/components/chat/providers'

export interface SessionIdState {
  /** Current value (raw, untrimmed). */
  value: Accessor<string>
  setValue: (v: string) => void
  /** Validation error for the current value, or null when empty / valid. */
  error: Accessor<string | null>
  /** Trimmed value — empty string means "no session id". */
  trimmed: Accessor<string>
  /**
   * True when the selected provider's session is a FILE, so its handle may be
   * a path. Derived once here and read by the input for its placeholder, so
   * the rule and the label state the same fact.
   */
  isFilePath: Accessor<boolean>
}

/**
 * Reactive state for an optional "resume an existing session" input.
 * The trimmed value collapses leading/trailing whitespace away so callers
 * can use `state.trimmed()` directly as the wire payload.
 *
 * The rule follows the PROVIDER, because a resume handle is not one shape:
 * Claude, Codex, ZCode and the ACP providers issue an opaque token, and Pi
 * accepts a session file path as well as an ID. The worker decides this per
 * provider too (`Provider.ValidateResumeHandle` in Go). While this field
 * applied the token rule to every provider, the two disagreed about Pi in both
 * directions: the browser refused a real session path at the 128-byte token
 * cap, and the worker refused the session ID the browser accepted. Neither
 * shape could be submitted.
 */
export function createSessionIdState(provider: Accessor<AgentProvider | undefined>): SessionIdState {
  const [value, setValue] = createSignal('')
  const trimmed = createMemo(() => value().trim())
  const isFilePath = createMemo(() => !!pluginFor(provider())?.sessionIdIsFilePath)
  const error = createMemo(() => {
    const v = trimmed()
    if (!v)
      return null
    return isFilePath() ? validateSessionFileHandle(v) : validateSessionId(v)
  })
  return { value, setValue, error, trimmed, isFilePath }
}
