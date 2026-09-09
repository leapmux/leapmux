import type { ControlAnswerState, Question } from '../../controls/types'
import type { ControlResponseDisplay, PersistedControlResponse } from '../../persistedControlResponse'
import type { PillOptions } from '~/components/common/PillGroup'
import { disambiguateLabels, isPillOptions, PILL_OPTION_LIMIT } from '~/components/common/PillGroup'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'
import { decodeControlBehaviorEnvelope } from '~/utils/controlResponse'
import { sendJsonRpcResult, sendResponse } from '../../controls/types'
import { feedback, firstNonEmpty, joinAnswerLines, label, labeledAnswerLine, labelOrNull } from '../../persistedControlResponse'

export type CodexDecision
  = | 'accept'
    | 'acceptForSession'
    | 'decline'
    | 'cancel'
    | { acceptWithExecpolicyAmendment: { execpolicy_amendment: string[] } }
    | { applyNetworkPolicyAmendment: { network_policy_amendment: { host: string, action: 'allow' | 'deny' } } }

/** Parses one exact decision variant from provider data. */
export function parseCodexDecision(value: unknown): CodexDecision | null {
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
    const amendment = isObject(body) ? body.network_policy_amendment : undefined
    if (isObject(amendment)
      && typeof amendment.host === 'string'
      && (amendment.action === 'allow' || amendment.action === 'deny')) {
      return value as CodexDecision
    }
  }
  return null
}

/** Gives the shared live-action and persisted-response label for a Codex decision. */
export function codexDecisionLabel(value: unknown): string {
  const decision = parseCodexDecision(value)
  if (!decision)
    return 'Unknown decision'
  if (typeof decision === 'string') {
    switch (decision) {
      case 'accept': return 'Allow'
      case 'acceptForSession': return 'Allow for Session'
      case 'decline': return 'Reject'
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

/** Gives a stable test key for a Codex decision button. */
export function codexDecisionKey(value: unknown): string {
  const decision = parseCodexDecision(value)
  if (!decision)
    return 'unknown'
  if (typeof decision === 'string')
    return decision
  return Object.keys(decision)[0]
}

/** Extract Codex approval params from the control request payload. */
export function getCodexParams(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  return pickObject(payload, 'params', undefined)
}

/**
 * Sends a Codex-native approval decision as a JSON-RPC response directly.
 */
export function sendCodexDecision(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  decision: CodexDecision,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { decision })
}

export function markCodexPlanPromptResponse(response: Record<string, unknown>): Record<string, unknown> {
  return { ...response, codexPlanModePrompt: true }
}

export function sendCodexPlanPromptResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  response: Record<string, unknown>,
): Promise<void> {
  return sendResponse(onRespond, markCodexPlanPromptResponse(response))
}

const CODEX_OTHER_OPTION_LABEL = 'None of the above'

function hasCodexOtherOption(question: Question): boolean {
  const raw = question as unknown as Record<string, unknown>
  return raw.isOther === true && Array.isArray(question.options) && question.options.length > 0
}

function codexAnswerValues(question: Question, index: number, answerState: ControlAnswerState): string[] {
  const selected = answerState.selections()[index] ?? []
  const customText = answerState.customTexts()[index]?.trim()
  const values = [...selected]

  if (customText) {
    if (values.length === 0 && hasCodexOtherOption(question)) {
      // Codex marks its auto-added free-form option explicitly.
      values.push(CODEX_OTHER_OPTION_LABEL)
    }
    // Codex's TUI appends free-form text as a user_note answer entry,
    // even for questions without a selected option.
    values.push(`user_note: ${customText}`)
  }

  return values
}

/**
 * Sends a Codex-native requestUserInput response as a JSON-RPC response directly.
 */
export function sendCodexUserInputResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  questions: Question[],
  answerState: ControlAnswerState,
): Promise<void> {
  const answers: Record<string, { answers: string[] }> = {}
  for (let i = 0; i < questions.length; i++) {
    const values = codexAnswerValues(questions[i], i, answerState)
    const key = questions[i].id || questions[i].header || `q${i}`
    answers[key] = { answers: values }
  }
  return sendJsonRpcResult(onRespond, requestId, { answers })
}

export function sendCodexUserInputRejectResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { answers: {} })
}

export function sendCodexPermissionsResponse(
  onRespond: (content: Uint8Array) => Promise<void>,
  requestId: string,
  permissions: Record<string, unknown>,
  scope: 'turn' | 'session',
): Promise<void> {
  return sendJsonRpcResult(onRespond, requestId, { permissions, scope })
}

function isNegativeDecision(decision: CodexDecision): boolean {
  if (decision === 'decline' || decision === 'cancel')
    return true
  return typeof decision === 'object'
    && 'applyNetworkPolicyAmendment' in decision
    && decision.applyNetworkPolicyAmendment.network_policy_amendment.action === 'deny'
}

interface CodexAllowChoice {
  key: string
  label: string
  decision: CodexDecision
}

/**
 * How each allow decision reads as a pill, strongest-lasting last.
 *
 * ONE ordered table, so a pill's label and its position come from one row. The
 * two lived in separate cascades that a reader had to keep in step by hand, and
 * neither was exhaustive: a decision that matched no branch still drew a pill,
 * labelled as the branch that happened to be last. `detail` tells two decisions
 * of the same row apart, because a payload may carry two host rules or two
 * command rules.
 */
const CODEX_ALLOW_CHOICE_SPECS: ReadonlyArray<{
  match: (decision: CodexDecision) => boolean
  label: string
  detail: (decision: CodexDecision) => string
}> = [
  { match: decision => decision === 'accept', label: 'Once', detail: () => 'Once' },
  { match: decision => decision === 'acceptForSession', label: 'Session', detail: () => 'Session' },
  {
    match: decision => typeof decision === 'object' && 'acceptWithExecpolicyAmendment' in decision,
    label: 'Command rule',
    detail: decision => (typeof decision === 'object' && 'acceptWithExecpolicyAmendment' in decision
      ? `Rule: ${decision.acceptWithExecpolicyAmendment.execpolicy_amendment.join(' ')}`
      : 'Command rule'),
  },
  {
    match: decision => typeof decision === 'object' && 'applyNetworkPolicyAmendment' in decision,
    label: 'Host rule',
    detail: decision => (typeof decision === 'object' && 'applyNetworkPolicyAmendment' in decision
      ? `Host: ${decision.applyNetworkPolicyAmendment.network_policy_amendment.host}`
      : 'Host rule'),
  },
]

function codexAllowChoiceSpecIndex(decision: CodexDecision): number {
  return CODEX_ALLOW_CHOICE_SPECS.findIndex(spec => spec.match(decision))
}

/**
 * Builds the choices that qualify the shared Allow button. A group requires
 * Codex's one-turn `accept` decision, so its first and default pill is Once.
 *
 * A decision that matches no row draws no pill. It stays in `additional`, where
 * `codexDecisionLabel` gives it its own name on its own button.
 */
function codexAllowChoices(decisions: CodexDecision[]): PillChoices | undefined {
  const candidates = decisions
    .map((decision, sourceIndex) => ({ decision, sourceIndex, specIndex: codexAllowChoiceSpecIndex(decision) }))
    // A repeated string decision draws two pills whose keys differ and whose
    // names cannot: `detail` returns a constant for `accept` and
    // `acceptForSession`, so `disambiguateLabels` has nothing to tell them
    // apart. Answering either sends the same decision, so keep the first.
    .filter((candidate, index, all) => typeof candidate.decision !== 'string'
      || all.findIndex(other => other.decision === candidate.decision) === index)
    .filter(candidate => candidate.specIndex >= 0 && !isNegativeDecision(candidate.decision))
    .sort((a, b) => a.specIndex - b.specIndex)

  if (candidates.length < 2 || candidates[0]?.decision !== 'accept')
    return undefined

  const drawn = candidates.slice(0, PILL_OPTION_LIMIT)
  const labels = disambiguateLabels(
    drawn,
    candidate => CODEX_ALLOW_CHOICE_SPECS[candidate.specIndex]!.label,
    candidate => CODEX_ALLOW_CHOICE_SPECS[candidate.specIndex]!.detail(candidate.decision),
  )
  const choices = drawn.map(({ decision, sourceIndex }, index) => ({
    key: `codex-allow-${sourceIndex}`,
    label: labels[index]!,
    decision,
  }))
  // The slice already caps the count and the guard above sets the floor, so this
  // narrows the tuple type rather than rejecting anything. Keeping the pills and
  // the selection on ONE value is what matters: a group that vanished while
  // `selectedAllowChoice` still answered would send a rule nothing offered.
  const options = choices.map(choice => ({ key: choice.key, label: choice.label }))
  return isPillOptions(options) ? { choices, options } : undefined
}

/** The allow choices and the exact pill options they draw, built together. */
interface PillChoices {
  choices: CodexAllowChoice[]
  options: PillOptions<string>
}

/**
 * The accessible name of the Codex allow-choice group.
 *
 * Both Codex action components draw the group, and the E2E specs and the
 * `allowChoicePillGroup` test helper look it up by this name, so one spelling
 * keeps a rename from reaching some of those and missing the rest.
 */
export const ALLOW_AS_LABEL = 'Allow as'

/** How long a Codex permissions grant lasts. Fixed, unlike the decision pills. */
export const CODEX_PERMISSION_SCOPE_OPTIONS = [
  { key: 'turn', label: 'Once' },
  { key: 'session', label: 'Session' },
] as const satisfies PillOptions<string>

export interface ResolvedCodexDecisions {
  /** Absent when Codex offered no way to refuse. The banner then draws no Deny. */
  negative?: CodexDecision
  /** Absent when Codex offered no way to approve. The banner then draws no Allow. */
  positive?: CodexDecision
  allowChoices?: PillChoices
  additional: CodexDecision[]
}

/**
 * The decisions a Codex approval banner draws, from the request's own list.
 *
 * A polarity the list does not carry is ABSENT, and the banner draws no button
 * for it. It must never be invented: a fabricated `accept` answers a request
 * that offered no way to approve, and a fabricated `cancel` refuses one that
 * offered no way to refuse. Codex then acts on a decision the user never had.
 *
 * One case does invent a token, and it is the opposite failure. `parseCodexDecision`
 * DROPS a variant that this build does not know, so a list of one unknown allow
 * decision beside a refusal parses to the refusal alone. Removing Allow there
 * would leave the user unable to approve at all, for a request that Codex did
 * offer an approval for. So a list the parser NARROWED keeps the canonical
 * token of the missing polarity -- the same pair the empty-list branch below
 * synthesizes -- and only a list that survived intact reports the polarity as
 * genuinely absent.
 */
export function resolveCodexDecisions(raw: unknown): ResolvedCodexDecisions {
  const offered = Array.isArray(raw) ? raw : []
  const parsed = offered
    .map(parseCodexDecision)
    .filter((decision): decision is CodexDecision => decision !== null)
  const narrowed = parsed.length < offered.length
  const decisions: CodexDecision[] = parsed.length > 0 ? parsed : ['accept', 'cancel']
  const offeredNegative = decisions.find(isNegativeDecision)
  const offeredPositive = decisions.find(decision => decision === 'accept')
    ?? decisions.find(decision => !isNegativeDecision(decision))
  const negative = offeredNegative ?? (narrowed ? 'cancel' : undefined)
  const positive = offeredPositive ?? (narrowed ? 'accept' : undefined)
  const allowChoices = codexAllowChoices(decisions)
  const consumed = new Set<CodexDecision>(
    allowChoices?.choices.map(choice => choice.decision) ?? (positive ? [positive] : []),
  )
  if (negative)
    consumed.add(negative)
  const additional = decisions.filter(decision => !consumed.has(decision))
  return { negative, positive, allowChoices, additional }
}

export function codexRequestedPermissions(payload: Record<string, unknown>): Record<string, unknown> {
  const permissions = pickObject(getCodexParams(payload), 'permissions', undefined)
  if (!permissions)
    return {}
  const granted: Record<string, unknown> = {}
  if (isObject(permissions.network))
    granted.network = permissions.network
  if (isObject(permissions.fileSystem))
    granted.fileSystem = permissions.fileSystem
  return granted
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
function codexDecisionText(request: Record<string, unknown> | undefined, response: Record<string, unknown> | undefined): string | null {
  const result = pickObject(response, 'result', undefined)
  const decision = result?.decision
  if (typeof decision === 'string') {
    const trimmed = decision.trim()
    if (trimmed === 'decline' && pickString(request, 'method', '').endsWith('/requestApproval'))
      return 'Deny'
    const parsed = parseCodexDecision(trimmed)
    return parsed ? codexDecisionLabel(parsed) : null
  }
  const parsed = parseCodexDecision(decision)
  return parsed ? codexDecisionLabel(parsed) : null
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
  return labelOrNull(codexDecisionText(cr.request, cr.response))
}
