import type { ParsedMessageContent } from '~/lib/messageParser'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { isObject, pickString, stringArray } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'

// This module reads worker metadata separately from the native request and response.
// Provider plugins derive display text from the native payloads.

// The user-facing labels for a persisted control-response answer, derived by the frontend from the
// native response payload (issue #258). They live in THIS leaf -- the module that owns
// control-response display -- shared by the transcript row renderer (renderControlResponseRow in
// messageRenderers, which imports CONTROL_RESPONSE_FEEDBACK_LEAD) AND the scroll-rail dot preview
// (controlResponsePreviewText below), so the dot reads IDENTICALLY to the row it jumps to and the
// wording lives in one place instead of being mirrored by hand across the two surfaces.
/**
 * The words a saved decision shows are the words its own BUTTON carried, so one answer
 * reads the same before and after the reader gives it. The two pairs below are the two
 * control families that state no option list of their own:
 *
 *   - a bare permission, whose buttons are Allow and Deny (`GenericToolActions`);
 *   - a plan approval, whose buttons are Approve and Reject (`ExitPlanModeControl`).
 *
 * A permission that DOES carry an option list reads its own option back instead, through
 * `permissionOptionLabel`.
 */
export const CONTROL_DECISION_WORDS = {
  permission: { allow: 'Allow', deny: 'Deny' },
  plan: { allow: 'Approve', deny: 'Reject' },
} as const

/** The pair of words one saved decision chooses between. */
export type ControlDecisionWords = typeof CONTROL_DECISION_WORDS[keyof typeof CONTROL_DECISION_WORDS]
/** Lead-in shown above the user's typed rejection reason (their feedback follows as markdown). */
export const CONTROL_RESPONSE_FEEDBACK_LEAD = 'Sent feedback:'
/**
 * Last-resort label when a plugin can't derive one from the native response and the coarse
 * behavior envelope isn't decodable either (an unknown provider string, a malformed payload).
 */
const CONTROL_RESPONSE_GENERIC_LABEL = 'Responded'

/** A response resolved from the original message and its separate request context. */
export interface PersistedControlResponse {
  requestId: string
  claimToken: string
  /** Complete native request, when available. */
  request: Record<string, unknown> | undefined
  /** Native response, or undefined when the original bytes are not a JSON object. */
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

/** Resolve one control response without adding fields to either provider payload. */
export function parsePersistedControlResponse(
  parsed: ParsedMessageContent | null | undefined,
): PersistedControlResponse | null {
  if (!parsed || parsed.wrapper || !isObject(parsed.messageMetadata))
    return null
  const requestId = parsed.messageMetadata[MESSAGE_METADATA_FIELD.ControlRequestID]
  if (typeof requestId !== 'string')
    return null
  return {
    requestId,
    claimToken: pickString(parsed.messageMetadata, MESSAGE_METADATA_FIELD.ControlRequestClaimToken),
    request: isObject(parsed.supplementalContent) ? parsed.supplementalContent : undefined,
    response: parsed.parentObject,
  }
}

/**
 * Coarse display from the neutral behavior envelope (`{response:{response:{behavior, message}}}`):
 * allow -> the positive word; deny with typed feedback -> that feedback; bare deny -> the negative
 * word. Null when `response` isn't that envelope (e.g. a JSON-RPC decision a provider plugin reads
 * instead). This is ALSO Claude's whole derivation -- its native response IS this envelope.
 *
 * `words` states which control the answer belongs to, because the envelope itself does not: a plan
 * approval and a bare permission share it, and their buttons say different things. The caller knows
 * which request it holds; this function cannot.
 */
export function controlBehaviorDisplay(
  response: unknown,
  words: ControlDecisionWords = CONTROL_DECISION_WORDS.permission,
): ControlResponseDisplay | null {
  const env = decodeControlBehaviorEnvelope(response)
  if (!env)
    return null
  if (env.behavior === 'allow')
    return label(words.allow)
  return feedbackOrLabel(env.message, words.deny)
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
    console.warn('Failed to derive the control response display.', { requestId: cr.requestId, err })
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
