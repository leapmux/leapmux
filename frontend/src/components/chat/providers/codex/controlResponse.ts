import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import { feedback, firstNonEmpty, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'

/** A Codex approval decision: a bare string (`accept`, `decline`, ...) or an amendment object. */
export type CodexDecision = string | Record<string, unknown>

/**
 * The human label for a Codex decision. The SINGLE source of truth for these labels: the live
 * approval buttons (CodexControlRequest) and the persisted-row derivation
 * ({@link codexControlResponseDisplay}) both call it, so a rendered answer reads identically to the
 * button the user clicked.
 */
export function codexDecisionLabel(decision: CodexDecision): string {
  if (typeof decision === 'string') {
    switch (decision) {
      case 'accept': return 'Allow'
      case 'acceptForSession': return 'Allow for Session'
      case 'decline': return 'Reject'
      case 'cancel': return 'Cancel'
      default: return decision
    }
  }
  // Guard the `in` reads with isObject: `decision` is typed string | object, but the live-button
  // path casts it straight from wire bytes (`params.availableDecisions as CodexDecision[]`), so a
  // malformed entry (null / a number) would make `'x' in decision` throw a TypeError and crash the
  // control banner render. Any non-string, non-amendment value falls through to the neutral "Allow".
  if (isObject(decision)) {
    if ('acceptWithExecpolicyAmendment' in decision)
      return 'Allow & Remember'
    if ('applyNetworkPolicyAmendment' in decision)
      return 'Apply Network Policy'
  }
  return 'Allow'
}

/**
 * The stable `data-testid` key for a Codex decision button: the decision string, or the first key of
 * an amendment object. Total like {@link codexDecisionLabel} -- the live-button path casts
 * `decision` straight from wire bytes (`params.availableDecisions as CodexDecision[]`), so a
 * malformed entry (null / a number / an empty object) must degrade to 'unknown' rather than throw a
 * TypeError on `Object.keys(null)` and crash the control-banner render.
 */
export function codexDecisionKey(decision: CodexDecision): string {
  if (typeof decision === 'string')
    return decision
  if (isObject(decision))
    return Object.keys(decision)[0] ?? 'unknown'
  return 'unknown'
}

/**
 * Render a requestUserInput answer as "Header: v1, v2" lines, in request-question order, then any
 * answer keys not in the request in a STABLE (sorted) order. Empty answer values are dropped (and
 * their key isn't marked seen), so an all-empty answer produces no line. Null when nothing renders.
 */
function codexUserInputAnswers(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  const answers = pickObject(result, 'answers', undefined)
  if (!answers || Object.keys(answers).length === 0)
    return null

  const params = pickObject(request, 'params', undefined)
  const questions = Array.isArray(params?.questions) ? params.questions : []

  const labels = new Map<string, string>()
  const order: string[] = []
  for (const q of questions) {
    if (!isObject(q))
      continue
    const key = firstNonEmpty(pickString(q, 'id', ''), pickString(q, 'header', ''))
    if (!key)
      continue
    labels.set(key, firstNonEmpty(pickString(q, 'header', ''), key))
    order.push(key)
  }

  const lines: string[] = []
  const seen = new Set<string>()
  const appendLine = (key: string): void => {
    const entry = answers[key]
    if (!isObject(entry) || seen.has(key))
      return
    // seen is set ONLY when a non-empty line is emitted, so the empty-filter and the dedup stay
    // entangled -- an all-empty answer neither renders nor marks the key seen.
    const line = labeledAnswerLine(labels.get(key) ?? key, entry.answers)
    if (line !== null) {
      lines.push(line)
      seen.add(key)
    }
  }

  for (const key of order)
    appendLine(key)
  // Answer keys absent from the request's questions render after them, sorted for stable output.
  for (const key of Object.keys(answers).filter(k => !seen.has(k)).sort())
    appendLine(key)

  return joinAnswerLines(lines)
}

/**
 * Read `result.decision` and map it to a label (the frontend now owns this; the backend persists
 * the native decision without deriving a label). Null for a missing/null/empty decision so the
 * caller degrades gracefully.
 */
function codexDecisionText(response: Record<string, unknown> | undefined): string | null {
  const result = pickObject(response, 'result', undefined)
  const decision = result?.decision
  if (typeof decision === 'string') {
    const trimmed = decision.trim()
    return trimmed ? codexDecisionLabel(trimmed) : null
  }
  if (isObject(decision))
    return Object.keys(decision).length > 0 ? codexDecisionLabel(decision) : null
  return null
}

/**
 * Derive the display for a persisted Codex control response, dispatching on the RESPONSE shape (not
 * the pruned request) so a request-gone answer still renders: requestUserInput answers live entirely
 * in `result.answers`, so `codexUserInputAnswers` recognizes them regardless of whether the pruned
 * request survived (it labels by question header when the request is present, else by the answer
 * key). A declined/stopped requestUserInput carries no answers -- it arrives as a JSON-RPC decision
 * ({result:{decision:'decline'}}) -- so it falls through to the deny-with-feedback / decision-label
 * derivation. Null when none applies (the caller falls back to the neutral behavior/generic label).
 */
export function codexControlResponseDisplay(cr: PersistedControlResponse): ControlResponseDisplay | null {
  const answers = codexUserInputAnswers(cr.request, cr.response)
  if (answers !== null)
    return label(answers)
  const env = decodeControlBehaviorEnvelope(cr.response)
  if (env?.message)
    return feedback(env.message)
  return labelOrNull(codexDecisionText(cr.response))
}
