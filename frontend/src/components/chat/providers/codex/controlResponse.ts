import type { ControlResponseSummary } from '../../model/controlResponse'
import type { ControlQuestion } from '../../model/question'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { isObject, pickObject } from '~/lib/jsonPick'
import { controlBehaviorDisplay, controlDecisionWords, firstNonEmpty, joinAnswerLines, label, labeledAnswerLine } from '../../persistedControlResponse'
import { codexAsyncQuestionSummary } from './asyncQuestion'

export type CodexDecision
  = | 'accept'
    | 'acceptForSession'
    | 'decline'
    | 'cancel'
    | { acceptWithExecpolicyAmendment: { execpolicy_amendment: string[] } }
    | { applyNetworkPolicyAmendment: { network_policy_amendment: { host: string, action: 'allow' | 'deny' } } }

/** Parses one exact decision variant from provider data. */
function parseCodexDecision(value: unknown): CodexDecision | null {
  if (value === 'accept' || value === 'acceptForSession' || value === 'decline' || value === 'cancel')
    return value
  if (!isObject(value) || Object.keys(value).length !== 1)
    return null
  if ('acceptWithExecpolicyAmendment' in value) {
    const body = value.acceptWithExecpolicyAmendment
    if (isObject(body) && Array.isArray(body.execpolicy_amendment) && body.execpolicy_amendment.every(part => typeof part === 'string'))
      return value as CodexDecision
    return null
  }
  if ('applyNetworkPolicyAmendment' in value) {
    const body = value.applyNetworkPolicyAmendment
    const amendment = isObject(body)
      ? body.network_policy_amendment
      : undefined
    if (isObject(amendment)
      && typeof amendment.host === 'string'
      && (amendment.action === 'allow' || amendment.action === 'deny')) {
      return value as CodexDecision
    }
  }
  return null
}

/** Gives the label that a saved Codex decision shows. */
export function codexDecisionLabel(value: unknown): string {
  const decision = parseCodexDecision(value)
  if (!decision)
    return 'Unknown decision'
  if (typeof decision === 'string') {
    switch (decision) {
      case 'accept': return 'Allow'
      case 'acceptForSession': return 'Allow for Session'
      case 'decline': return 'Deny'
      case 'cancel': return 'Cancel'
      default: return decision
    }
  }
  if ('acceptWithExecpolicyAmendment' in decision)
    return 'Allow & Remember'
  return decision.applyNetworkPolicyAmendment.network_policy_amendment.action === 'allow'
    ? 'Allow Host & Remember'
    : 'Block Host & Remember'
}

/**
 * Format requestUserInput answers as Header: v1, v2 lines.
 * Use the order of the recorded prompt's questions first, then sort remaining answer keys.
 * Drop empty answer values without marking their keys seen.
 * Return null when no answer line remains.
 *
 * The Worker keys each answer by its question's id, else its header, else `q<index>`
 * (`codexUserInputAnswer` in Go), and the index is the question's place in the prompt.
 */
function codexUserInputAnswers(
  questions: readonly ControlQuestion[],
  response: Record<string, unknown> | undefined,
): string | null {
  const result = pickObject(response, 'result', undefined)
  const answers = pickObject(result, 'answers', undefined)
  if (!answers || Object.keys(answers).length === 0)
    return null

  const labels = new Map<string, string>()
  const order: string[] = []
  questions.forEach((question, index) => {
    const key = firstNonEmpty(question.id, question.header) || `q${index}`
    labels.set(key, firstNonEmpty(question.header, key))
    order.push(key)
  })

  const lines: string[] = []
  const seen = new Set<string>()
  const appendLine = (key: string): void => {
    // `hasOwn`, never a bare lookup: the key is an id or a header that the model
    // wrote, and one that spells an `Object.prototype` member would read that member.
    const entry = Object.hasOwn(answers, key)
      ? answers[key]
      : undefined
    if (!isObject(entry) || seen.has(key))
      return
    // Mark a key seen only after producing a nonempty answer line.
    // An empty answer must produce no line and must not prevent a later nonempty answer.
    const line = labeledAnswerLine(labels.get(key) ?? key, entry.answers)
    if (line !== null) {
      lines.push(line)
      seen.add(key)
    }
  }

  for (const key of order)
    appendLine(key)
  // Answer keys absent from the prompt's questions render after them, sorted for stable output.
  for (const key of Object.keys(answers).filter(k => !seen.has(k)).sort())
    appendLine(key)

  return joinAnswerLines(lines)
}

/**
 * Read result.decision and derive its label in the frontend.
 * The backend stores the native decision without adding a label.
 * Return null for a missing, null, or empty decision so the caller can use its fallback.
 */
function codexDecisionText(response: Record<string, unknown> | undefined): string | null {
  const result = pickObject(response, 'result', undefined)
  const decision = result?.decision
  const parsed = parseCodexDecision(typeof decision === 'string'
    ? decision.trim()
    : decision)
  return parsed
    ? codexDecisionLabel(parsed)
    : null
}

/**
 * Read the response shape to derive a persisted Codex answer.
 *
 * - LeapMux's own async question answers in the neutral envelope, and its display is
 *   the quoted question with the answer.
 * - A requestUserInput answer states result.answers, labeled by the questions of the
 *   recorded prompt. A row whose prompt states no question labels them by their keys.
 * - An approval states the native result.decision.
 * - The plan-mode prompt answers in the neutral envelope. The envelope does not state
 *   which control it answers, so its words come from the recorded prompt: a plan takes
 *   the plan words, a typed refusal its feedback.
 *
 * Return null when no response-specific display applies, so the caller can use its fallback.
 */
export function codexControlResponseSummary(cr: PersistedControlResponse): ControlResponseSummary | null {
  const asyncQuestion = codexAsyncQuestionSummary(cr)
  if (asyncQuestion !== null)
    return asyncQuestion
  const questions = cr.prompt?.kind === 'question'
    ? cr.prompt.questions
    : []
  const answers = codexUserInputAnswers(questions, cr.response)
  if (answers !== null)
    return label(answers)
  const decision = codexDecisionText(cr.response)
  if (decision !== null)
    return label(decision)
  return controlBehaviorDisplay(cr.response, controlDecisionWords(cr.prompt))
}
