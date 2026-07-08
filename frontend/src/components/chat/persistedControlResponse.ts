import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'

// ---------------------------------------------------------------------------
// Persisted control-response row -- shared parsing + neutral fallback (issue #258)
//
// A LEAF module (only pure json/text utils + the label constants) so provider plugins, the
// transcript renderer (messageRenderers), and the scroll-rail preview (chatMarkPreview) can all
// import it without pulling in the plugin registry -- that would be a module-init cycle.
//
// The backend persists every non-self-displayed control answer as
//   {isSynthetic:true, controlResponse:{provider, requestId, request, response}}
// where `response` is the provider-native payload sent to the agent and `request` is the minimal
// render context the backend snapshotted before deleting the pending request. This module parses
// that envelope and provides the provider-NEUTRAL fallback derivation; each provider plugin's
// `controlResponseDisplay` owns its own native wire shapes and degrades to `fallback*` here.
// ---------------------------------------------------------------------------

// The user-facing labels for a persisted control-response answer, derived by the frontend from the
// native response payload (issue #258). They live in THIS leaf -- the module that owns
// control-response display -- shared by the transcript row renderer (renderControlResponseRow in
// messageRenderers, which imports CONTROL_RESPONSE_FEEDBACK_LEAD) AND the scroll-rail dot preview
// (controlResponsePreviewText below), so the dot reads IDENTICALLY to the row it jumps to and the
// wording lives in one place instead of being mirrored by hand across the two surfaces.
/** An approved control request (ExitPlanMode approval, an "allow" permission decision). */
const CONTROL_RESPONSE_APPROVED_LABEL = 'Approved'
/** A declined control request with no typed reason. */
const CONTROL_RESPONSE_REJECTED_LABEL = 'Rejected'
/** Lead-in shown above the user's typed rejection reason (their feedback follows as markdown). */
export const CONTROL_RESPONSE_FEEDBACK_LEAD = 'Sent feedback:'
/**
 * Last-resort label when a plugin can't derive one from the native response and the coarse
 * behavior envelope isn't decodable either (an unknown provider string, a malformed payload).
 */
const CONTROL_RESPONSE_GENERIC_LABEL = 'Responded'

/**
 * The parsed neutral envelope of a persisted synthetic control-response row. Fields are
 * tolerant-optional: a malformed or legacy row still parses so the caller can degrade via
 * {@link fallbackControlResponseDisplay} rather than throwing.
 */
export interface PersistedControlResponse {
  /** AgentProvider enum name minus its `AGENT_PROVIDER_` prefix (CODEX, OPENCODE, ...); '' when absent. */
  provider: string
  /** Id of the control request this answers; '' when absent. */
  requestId: string
  /** Minimal provider-pruned request context, or undefined when the row omitted it. */
  request: Record<string, unknown> | undefined
  /** The provider-native response payload as sent to the agent, or undefined when malformed. */
  response: Record<string, unknown> | undefined
}

/**
 * What a provider derives from a persisted control response.
 * - `label`: short plain text, possibly multi-line (`\n`-joined answer lines). The row renders it
 *   with line breaks preserved; the rail truncates it verbatim.
 * - `feedback`: the user's typed deny reason. The row renders it as markdown under
 *   {@link CONTROL_RESPONSE_FEEDBACK_LEAD}; the rail shows the lead + reason.
 */
export type ControlResponseDisplay
  = | { kind: 'label', text: string }
    | { kind: 'feedback', message: string }

/**
 * A provider's persisted-control-response derivation: native payload -> display, or null when the
 * payload isn't recognizable (the caller then degrades via {@link fallbackControlResponseDisplay}).
 * Named once in this leaf so the registry interface, the transcript renderer, and the
 * {@link resolveControlResponseDisplay} chokepoint reference ONE spelling instead of re-typing the
 * signature -- a change to the contract lands in one place. (registerACPProvider reaches for
 * `Provider['controlResponseDisplay']` instead, since it can import the plugin type without a cycle.)
 */
export type ControlResponseDeriver = (cr: PersistedControlResponse) => ControlResponseDisplay | null

/**
 * Build a `label` display from plain text -- the factory for the tagged union's `{ kind: 'label' }`
 * variant, so that shape is spelled once here instead of inline at every derivation's return
 * (Codex answer lines, Cursor "Accept", Pi confirm/value, the neutral Approved/Rejected/Responded).
 */
export function label(text: string): ControlResponseDisplay {
  return { kind: 'label', text }
}

/**
 * Lift a plain-text label into a `ControlResponseDisplay`, or null when there is no meaningful
 * label. The provider derivations each produce a `string | null` answer and share this one wrap: a
 * null answer maps to null, and so does an EMPTY string -- an empty label would render a blank row
 * (and a blank rail-dot preview), so it degrades to null and the caller falls back to the neutral
 * behavior/generic label instead. No current derivation returns '' (they return null or a non-empty
 * line via joinAnswerLines / a guarded optionId), so this only guards a future one.
 */
export function labelOrNull(text: string | null): ControlResponseDisplay | null {
  return text ? label(text) : null
}

/**
 * Build a `feedback` display from the user's typed reason -- the sibling factory to
 * {@link label} for the other variant of the tagged union, so the `{ kind: 'feedback' }`
 * shape is spelled once instead of inline at every deny-with-reason site (Claude/Codex behavior
 * envelope, Cursor question/plan rejections).
 */
export function feedback(message: string): ControlResponseDisplay {
  return { kind: 'feedback', message }
}

/**
 * A typed reason renders as `feedback` (shown under {@link CONTROL_RESPONSE_FEEDBACK_LEAD}), else the
 * bare `fallbackLabel`. The single home for the "reason -> feedback, else label" rule the neutral
 * behavior envelope (bare deny -> "Rejected") and the Cursor question-cancel / plan reject-cancel
 * outcomes all share, so the deny-with-feedback wording can't drift between them.
 */
export function feedbackOrLabel(reason: string, fallbackLabel: string): ControlResponseDisplay {
  return reason ? feedback(reason) : label(fallbackLabel)
}

/**
 * Envelope predicate shared by every provider's classify: a synthetic row whose `controlResponse`
 * is an object (the shape persistControlResponseRow writes). Keeping it here means each plugin's
 * classify delegates to one definition rather than re-hardcoding the shape.
 */
export function isPersistedControlResponse(
  obj: unknown,
): obj is { isSynthetic: true, controlResponse: Record<string, unknown> } {
  return isObject(obj) && obj.isSynthetic === true && isObject(obj.controlResponse)
}

/** Parse the envelope; null when the shape isn't a persisted control response. */
export function parsePersistedControlResponse(
  obj: unknown,
): PersistedControlResponse | null {
  if (!isPersistedControlResponse(obj))
    return null
  const cr = obj.controlResponse
  return {
    provider: pickString(cr, 'provider', ''),
    requestId: pickString(cr, 'requestId', ''),
    request: isObject(cr.request) ? cr.request : undefined,
    response: isObject(cr.response) ? cr.response : undefined,
  }
}

/**
 * Coarse display from the neutral behavior envelope (`{response:{response:{behavior, message}}}`):
 * allow -> "Approved"; deny with typed feedback -> that feedback; bare deny -> "Rejected". Null when
 * `response` isn't that envelope (e.g. a JSON-RPC decision a provider plugin reads instead). This is
 * ALSO Claude's whole derivation -- its native response IS this envelope.
 */
export function controlBehaviorDisplay(response: unknown): ControlResponseDisplay | null {
  const env = decodeControlBehaviorEnvelope(response)
  if (!env)
    return null
  if (env.behavior === 'allow')
    return label(CONTROL_RESPONSE_APPROVED_LABEL)
  return feedbackOrLabel(env.message, CONTROL_RESPONSE_REJECTED_LABEL)
}

/**
 * Graceful degradation when a plugin can't derive a label from its native response: fall back to the
 * coarse behavior envelope, else the generic {@link CONTROL_RESPONSE_GENERIC_LABEL}. Never null, so
 * a control-response row always renders SOMETHING.
 */
export function fallbackControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay {
  return controlBehaviorDisplay(cr.response) ?? label(CONTROL_RESPONSE_GENERIC_LABEL)
}

/**
 * Resolve a persisted control response to its never-null display -- the SINGLE chokepoint both
 * surfaces that render the row go through (the transcript renderer and the scroll-rail dot preview),
 * so they can't drift or forget the fallback. Runs the provider's derivation, degrades to
 * {@link fallbackControlResponseDisplay} when it returns null, AND catches a derivation that THROWS
 * on a malformed payload -- so neither surface can leak raw wire bytes or render nothing.
 */
export function resolveControlResponseDisplay(
  cr: PersistedControlResponse,
  display: ControlResponseDeriver | undefined,
): ControlResponseDisplay {
  // ONLY the provider derivation is untrusted, so it is the only thing inside the try. The
  // fallback runs OUTSIDE it -- once, on both the returned-null and the threw paths -- so a
  // future non-total fallback can never double-throw and escape this never-null chokepoint.
  try {
    const derived = display?.(cr)
    if (derived)
      return derived
  }
  catch (err) {
    // A total derivation should never throw (they are built from the tolerant pick*/isObject
    // helpers), so a throw here is a real derivation bug, not malformed data. Log it -- otherwise
    // the answer silently renders the generic fallback forever with no trace to the cause -- then
    // fall through to the same degrade below so neither surface leaks raw wire bytes.
    console.warn('control-response display derivation threw; degrading to fallback', { provider: cr.provider, err })
  }
  return fallbackControlResponseDisplay(cr)
}

/**
 * Plaintext projection shared by the rail preview: a label renders verbatim; feedback renders the
 * lead + the reason on the next line (the rail then truncates the whole thing).
 */
export function controlResponsePreviewText(display: ControlResponseDisplay): string {
  return display.kind === 'feedback'
    ? `${CONTROL_RESPONSE_FEEDBACK_LEAD}\n${display.message}`
    : display.text
}

/**
 * The first argument non-empty after trimming (and the chosen value is trimmed), or '' when all are
 * blank. The per-provider answer derivations use it to pick a label (`header` else `id`, `prompt`
 * else `id`, ...).
 */
export function firstNonEmpty(...vals: Array<string | undefined>): string {
  for (const v of vals) {
    const t = (v ?? '').trim()
    if (t)
      return t
  }
  return ''
}

/**
 * Format one "label: v1, v2" line, trimming each value and dropping the empties; null when no value
 * survives (the caller skips the line). Non-string entries are ignored, matching the string-typed
 * answer arrays every provider produces.
 */
export function labeledAnswerLine(labelText: string, values: unknown): string | null {
  const parts = stringArray(values)
    .map(v => v.trim())
    .filter(v => v !== '')
  if (parts.length === 0)
    return null
  return `${labelText}: ${parts.join(', ')}`
}

/**
 * Join collected answer lines into the multi-question label text, or null when none survived -- the
 * shared "empty -> null, else newline-join" tail every provider's answer-line builder (Codex,
 * OpenCode, Cursor) ends with, so the join separator lives in one place instead of three.
 */
export function joinAnswerLines(lines: string[]): string | null {
  return lines.length > 0 ? lines.join('\n') : null
}
