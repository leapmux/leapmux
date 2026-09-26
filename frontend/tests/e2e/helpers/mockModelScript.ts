/**
 * The vocabulary a test injects into the mock model server.
 *
 * This module holds no prompt knowledge and no transport. The server
 * (`./mockModelServer`) parses a registration against it, and the test client
 * (`./mockModelScenario`) builds one. Keeping the vocabulary here lets both
 * sides state one shape, so a script that a test writes and a script that the
 * server accepts cannot drift.
 */

/**
 * The request shapes the installed coding agents send.
 *
 * `aws-event-stream` is the one vendor service among them: Kiro's own service, which
 * takes AWS JSON 1.0 and answers a turn with an AWS event stream. See
 * `./kiroSurface`.
 */
export type MockModelProtocol = 'openai-chat-completions' | 'openai-responses' | 'anthropic-messages' | 'aws-event-stream'

export const MOCK_MODEL_PROTOCOLS: readonly MockModelProtocol[] = [
  'openai-chat-completions',
  'openai-responses',
  'anthropic-messages',
  'aws-event-stream',
]

/** The reserved scenario that answers a request carrying no marker. */
export const AMBIENT_SCENARIO_ID = 'ambient'

/**
 * Use only alphanumeric text before the colon. A rich-text editor escapes
 * Markdown punctuation before the provider receives the user message.
 */
export const SCENARIO_MARKER = 'LEAPMUXE2ESCENARIO:'

export const SCENARIO_ID_PATTERN = /^[\w-]{1,128}$/

/** The longest a step may hold its response open, so a stuck script cannot hang the run. */
export const MAX_STEP_DELAY_MS = 120_000

/**
 * One tool call the model makes.
 *
 * A tool call states EITHER JSON `arguments`, for an ordinary function tool, OR
 * raw `input`, for an OpenAI CUSTOM tool. Codex's `exec` is the custom one: it
 * takes JavaScript source text rather than a JSON object, so a JSON-argument
 * call would reach its runtime as an unparsable body.
 */
export interface MockModelToolCall {
  id: string
  name: string
  arguments?: Record<string, unknown>
  /** Raw text input. The OpenAI Responses protocol is the only one that carries it. */
  input?: string
  /**
   * The tool NAMESPACE, for a provider that groups its tools into several.
   *
   * Codex declares two -- `functions` holds `exec`, `wait` and
   * `request_user_input`, and `collaboration` holds `spawn_agent`, `wait_agent`
   * and the rest -- and a call into a non-default namespace must name it. A call
   * that omits it comes back as `unsupported call: <name>` in the tool OUTPUT,
   * so the turn continues and the only symptom is that the tool did nothing.
   *
   * The OpenAI Responses protocol is the only one that carries it.
   */
  namespace?: string
}

export interface MockModelError {
  status: number
  message: string
  code?: string
}

/**
 * Deliver a step's `text` in pieces rather than in one, pausing between them.
 *
 * A test that observes a turn MID-FLIGHT needs two things: a turn that lasts
 * long enough to catch, and output that GROWS while it runs. `delayMs` supplies
 * the first alone -- it holds the whole answer back and then delivers it in one
 * piece, so a progress indicator driven by token counts never moves and an
 * interrupt finds nothing to truncate.
 *
 * Only `text` is chunked. Reasoning and a tool call are single decisions, and a
 * client reassembles either one before it shows anything.
 */
export interface MockModelTextStream {
  /** Characters per piece. At least 1. */
  chunkChars: number
  /** Pause between pieces, under the same cap as `delayMs`. */
  delayMs: number
}

/**
 * Token counts a step reports in its usage block.
 *
 * Every field is optional. An absent field keeps the mock's default of 1, so a
 * step that only needs a bigger `inputTokens` need not state the rest.
 */
export interface MockModelUsage {
  inputTokens?: number
  outputTokens?: number
  /**
   * Context-window size the client may pair with `inputTokens`.
   *
   * Some CLIs surface context usage only when the response or their own catalog
   * states a window. The mock cannot change a catalog; this field feeds the
   * response shapes that carry a window alongside usage.
   */
  contextWindow?: number
}

/**
 * Rate-limit surface on one answer.
 *
 * The mock writes the standard provider response headers, so a CLI that parses
 * them can emit its own rate-limit state. A step that wants the request refused
 * states `error` with status 429 instead; that path is separate because an
 * error answer carries no output.
 */
export interface MockModelRateLimits {
  /** `five_hour`, `weekly`, `primary`, `secondary`, … The provider's own vocabulary. */
  type: string
  /** `allowed`, `exceeded`, `rate_limited`, `rejected`, … */
  status: string
  /** Fraction of the window used, from 0 to 1. */
  utilization?: number
  /** Unix seconds when the window resets. */
  resetsAt?: number
}

/** One model answer. A step carries output or an error, never both. */
export interface MockModelStep {
  /** The thinking the model reports before its answer. */
  reasoning?: string
  text?: string
  toolCalls?: MockModelToolCall[]
  error?: MockModelError
  /** Hold the answer open for this long. An interrupt test cancels inside that window. */
  delayMs?: number
  /** Deliver `text` progressively. See `MockModelTextStream`. */
  stream?: MockModelTextStream
  /** Token counts on the usage block. Defaults to 1 input and 1 output. */
  usage?: MockModelUsage
  /** Rate-limit headers and body fields on the answer. */
  rateLimits?: MockModelRateLimits
  /**
   * Values that the answer copies from the request, keyed by placeholder name.
   *
   * Each value is a regular-expression source with one capture group. The
   * server tests it against every string of the request body, and the LAST
   * match supplies the value. The server then replaces each `{{name}}` in
   * `text`, `reasoning`, and every tool-call string with that value.
   *
   * This exists for a value that the agent chooses at run time and the test
   * cannot know. Kimi Code is the case: it gives each plan a random file path
   * and states it only in its system reminder, and the model must write the
   * plan to that exact path. A capture that matches nothing makes the request
   * unexpected, so the turn fails with its body recorded.
   */
  captures?: Record<string, string>
}

/** `{{name}}`, where the name follows the rules of a JavaScript identifier. */
const CAPTURE_PLACEHOLDER = /\{\{([a-z_]\w*)\}\}/gi

/**
 * `text` split the way `stream` asks, or the whole of it as one piece.
 *
 * An absent `text` yields NO piece, which is what lets a tool-call-only step
 * write no text event at all.
 */
export function textChunks(step: MockModelStep): string[] {
  if (step.text === undefined)
    return []
  const size = step.stream?.chunkChars
  if (!size || step.text.length <= size)
    return [step.text]
  const chunks: string[] = []
  for (let at = 0; at < step.text.length; at += size)
    chunks.push(step.text.slice(at, at + size))
  return chunks
}

/**
 * One or more regular-expression sources, each compiled with the `i` flag.
 *
 * A prompt match is never case-significant, so the flag is fixed. An array
 * states several conditions on one field, and every one of them must match.
 * That keeps a rule readable where a single expression would need a lookahead.
 */
export type MockModelPattern = string | string[]

/**
 * A predicate over one model request.
 *
 * Every stated field must match. An empty matcher matches every request of the
 * scenario, which is how a catch-all rule is written.
 */
export interface MockModelMatcher {
  protocol?: MockModelProtocol
  /** Tested against the joined system text of the request. */
  system?: MockModelPattern
  /** Tested against the text of the last user turn. */
  user?: MockModelPattern
  /** Tested against the complete request body as JSON. */
  body?: MockModelPattern
}

/**
 * A repeatable answer for the requests a matcher selects.
 *
 * The server tries every rule before it consumes a step, so a provider's own
 * housekeeping turn — title generation, a summary, a compaction — never takes
 * the answer the test scripted for the next real turn.
 */
export interface MockModelRule {
  name: string
  when: MockModelMatcher
  respond: MockModelStep
  /** Answer at most once. A rule repeats by default. */
  once?: boolean
}

/** One complete script: an ordered queue, the rules that bypass it, and a fallback. */
export interface MockModelScenarioSpec {
  steps: MockModelStep[]
  rules: MockModelRule[]
  /**
   * The answer for a request that outlives the queue.
   *
   * Absent by default, which makes an unscripted turn a recorded failure. A
   * test states one when it cannot know how many turns follow — an approval
   * that restarts the agent, a provider that retries on its own.
   */
  fallback?: MockModelStep
}

export interface MockModelRequestRecord {
  protocol: MockModelProtocol
  path: string
  /** The index the request consumed, for a request that a rule did not answer. */
  stepIndex?: number
  /** The rule that answered, for a request that no step consumed. */
  rule?: string
  /** Set when the queue was exhausted and the fallback answered. */
  fallback?: true
  body: unknown
}

export interface MockModelUnexpectedRequest {
  protocol: MockModelProtocol
  path: string
  reason: string
  body: unknown
}

export interface MockModelScenarioStatus {
  complete: boolean
  nextStep: number
  stepCount: number
  /** How many requests each rule answered, keyed by rule name. */
  ruleMatches: Record<string, number>
  requests: MockModelRequestRecord[]
  unexpectedRequests: MockModelUnexpectedRequest[]
}

export function validateScenarioID(id: string): void {
  if (!SCENARIO_ID_PATTERN.test(id))
    throw new Error('A model scenario ID must use 1 to 128 ASCII letters, digits, underscores, or hyphens')
}

/**
 * Parse a registration body into a script.
 *
 * The server validates on the wire rather than trusting the client, so a
 * malformed script fails at registration with the field that is wrong, not
 * later as a provider turn that answers nothing.
 */
export function parseScenarioSpec(body: unknown): MockModelScenarioSpec {
  if (!isRecord(body))
    throw new Error('A model scenario must be an object')
  const stepsValue = body.steps ?? []
  if (!Array.isArray(stepsValue))
    throw new Error('A model scenario steps field must be an array')
  const rulesValue = body.rules ?? []
  if (!Array.isArray(rulesValue))
    throw new Error('A model scenario rules field must be an array')
  if (stepsValue.length === 0 && rulesValue.length === 0 && body.fallback === undefined)
    throw new Error('A model scenario needs at least one step, one rule, or a fallback')
  const steps = stepsValue.map((value, index) => parseStep(value, `step ${index}`))
  const rules = rulesValue.map((value, index) => parseRule(value, index))
  const fallback = body.fallback === undefined ? undefined : parseStep(body.fallback, 'fallback')
  const names = new Set<string>()
  for (const rule of rules) {
    if (names.has(rule.name))
      throw new Error(`Model rule ${rule.name} is declared twice`)
    names.add(rule.name)
  }
  return { steps, rules, ...(fallback ? { fallback } : {}) }
}

function parseRule(value: unknown, index: number): MockModelRule {
  if (!isRecord(value))
    throw new Error(`Model rule ${index} must be an object`)
  if (typeof value.name !== 'string' || !value.name)
    throw new Error(`Model rule ${index} needs a name`)
  if (value.once !== undefined && typeof value.once !== 'boolean')
    throw new Error(`Model rule ${value.name} once must be a boolean`)
  return {
    name: value.name,
    when: parseMatcher(value.when, value.name),
    respond: parseStep(value.respond, `rule ${value.name}`),
    ...(value.once === true ? { once: true } : {}),
  }
}

function parseMatcher(value: unknown, ruleName: string): MockModelMatcher {
  if (value === undefined)
    return {}
  if (!isRecord(value))
    throw new Error(`Model rule ${ruleName} when must be an object`)
  const matcher: MockModelMatcher = {}
  if (value.protocol !== undefined) {
    if (typeof value.protocol !== 'string' || !MOCK_MODEL_PROTOCOLS.includes(value.protocol as MockModelProtocol))
      throw new Error(`Model rule ${ruleName} protocol must be one of ${MOCK_MODEL_PROTOCOLS.join(', ')}`)
    matcher.protocol = value.protocol as MockModelProtocol
  }
  for (const field of ['system', 'user', 'body'] as const) {
    const declared = value[field]
    if (declared === undefined)
      continue
    const declaredList: unknown[] = Array.isArray(declared) ? declared : [declared]
    if (declaredList.length === 0)
      throw new Error(`Model rule ${ruleName} ${field} must state at least one pattern`)
    const patterns: string[] = []
    for (const pattern of declaredList) {
      if (typeof pattern !== 'string' || !pattern)
        throw new Error(`Model rule ${ruleName} ${field} must be a non-empty pattern`)
      try {
        void new RegExp(pattern, 'i')
      }
      catch (error) {
        throw new Error(`Model rule ${ruleName} ${field} is not a valid regular expression`, { cause: error })
      }
      patterns.push(pattern)
    }
    matcher[field] = Array.isArray(declared) ? patterns : patterns[0]!
  }
  return matcher
}

function parseStep(value: unknown, label: string): MockModelStep {
  if (!isRecord(value))
    throw new Error(`Model ${label} must be an object`)
  if (value.text !== undefined && typeof value.text !== 'string')
    throw new Error(`Model ${label} text must be a string`)
  if (value.reasoning !== undefined && typeof value.reasoning !== 'string')
    throw new Error(`Model ${label} reasoning must be a string`)
  const toolCallsValue = value.toolCalls
  if (toolCallsValue !== undefined && !Array.isArray(toolCallsValue))
    throw new Error(`Model ${label} toolCalls must be an array`)
  const toolCalls = Array.isArray(toolCallsValue)
    ? toolCallsValue.map((tool, toolIndex) => parseToolCall(tool, label, toolIndex))
    : undefined
  const error = value.error === undefined ? undefined : parseModelError(value.error, label)
  if (value.text === undefined && toolCalls === undefined && error === undefined)
    throw new Error(`Model ${label} needs text, toolCalls, or error`)
  if (error && (value.text !== undefined || toolCalls !== undefined || value.reasoning !== undefined))
    throw new Error(`Model ${label} cannot combine an error with output`)
  const delayMs = parseDelay(value.delayMs, label)
  const stream = parseTextStream(value.stream, label)
  if (stream && value.text === undefined)
    throw new Error(`Model ${label} states stream but no text to deliver`)
  const captures = parseCaptures(value.captures, label)
  if (captures && error)
    throw new Error(`Model ${label} cannot combine captures with an error`)
  const usage = parseUsage(value.usage, label)
  if (usage && error)
    throw new Error(`Model ${label} cannot combine usage with an error`)
  const rateLimits = parseRateLimits(value.rateLimits, label)
  if (rateLimits && error)
    throw new Error(`Model ${label} cannot combine rateLimits with an error`)
  const step: MockModelStep = {
    ...(typeof value.reasoning === 'string' ? { reasoning: value.reasoning } : {}),
    ...(typeof value.text === 'string' ? { text: value.text } : {}),
    ...(toolCalls ? { toolCalls } : {}),
    ...(error ? { error } : {}),
    ...(delayMs === undefined ? {} : { delayMs }),
    ...(stream === undefined ? {} : { stream }),
    ...(usage === undefined ? {} : { usage }),
    ...(rateLimits === undefined ? {} : { rateLimits }),
    ...(captures === undefined ? {} : { captures }),
  }
  if (captures) {
    // A placeholder with no capture would reach the agent as literal braces,
    // and a capture that no placeholder uses states a match that nothing reads.
    const used = new Set(stepPlaceholders(step))
    for (const name of used) {
      if (!Object.hasOwn(captures, name))
        throw new Error(`Model ${label} uses {{${name}}} but declares no capture for it`)
    }
    for (const name of Object.keys(captures)) {
      if (!used.has(name))
        throw new Error(`Model ${label} declares capture ${name} but no {{${name}}} placeholder uses it`)
    }
  }
  return step
}

function parseCaptures(value: unknown, label: string): Record<string, string> | undefined {
  if (value === undefined)
    return undefined
  if (!isRecord(value) || Object.keys(value).length === 0)
    throw new Error(`Model ${label} captures must be an object with at least one entry`)
  const captures: Record<string, string> = {}
  for (const [name, source] of Object.entries(value)) {
    if (!/^[a-z_]\w*$/i.test(name))
      throw new Error(`Model ${label} capture name ${name} must be an identifier`)
    if (typeof source !== 'string' || !source)
      throw new Error(`Model ${label} capture ${name} must be a non-empty pattern`)
    let pattern: RegExp
    try {
      pattern = new RegExp(source)
    }
    catch (error) {
      throw new Error(`Model ${label} capture ${name} is not a valid regular expression`, { cause: error })
    }
    // An alternation with the empty string always matches, so the match array
    // states how many groups the pattern declares.
    const groups = new RegExp(`${pattern.source}|`).exec('')!.length - 1
    if (groups !== 1)
      throw new Error(`Model ${label} capture ${name} must declare exactly one capture group, not ${groups}`)
    captures[name] = source
  }
  return captures
}

/** Every placeholder name that the step's strings use, with repeats. */
function stepPlaceholders(step: MockModelStep): string[] {
  const names: string[] = []
  visitStrings([step.text, step.reasoning, step.toolCalls], (text) => {
    for (const match of text.matchAll(CAPTURE_PLACEHOLDER))
      names.push(match[1]!)
  })
  return names
}

/** The step with each placeholder resolved, or the name of the capture that found no match. */
export type ResolvedStep = { step: MockModelStep } | { unmatchedCapture: string }

/**
 * Resolve a step's captures against one request body.
 *
 * A step without captures returns unchanged, so a script that states literal
 * braces in its text keeps them.
 */
export function resolveStepCaptures(step: MockModelStep, body: unknown): ResolvedStep {
  if (!step.captures)
    return { step }
  const values = new Map<string, string>()
  for (const [name, source] of Object.entries(step.captures)) {
    const pattern = new RegExp(source, 'g')
    let last: string | undefined
    visitStrings(body, (text) => {
      for (const match of text.matchAll(pattern)) {
        if (match[1] !== undefined)
          last = match[1]
      }
    })
    if (last === undefined)
      return { unmatchedCapture: name }
    values.set(name, last)
  }
  const fill = (text: string): string => text.replace(CAPTURE_PLACEHOLDER, (whole, name: string) => values.get(name) ?? whole)
  const resolved: MockModelStep = {
    ...step,
    ...(step.text === undefined ? {} : { text: fill(step.text) }),
    ...(step.reasoning === undefined ? {} : { reasoning: fill(step.reasoning) }),
    ...(step.toolCalls === undefined ? {} : { toolCalls: step.toolCalls.map(call => fillToolCall(call, fill)) }),
  }
  // The resolved step states literal values. Keeping the captures would let a
  // second resolution match the filled text against the patterns again.
  delete resolved.captures
  return { step: resolved }
}

/**
 * The call with each placeholder filled, in every string that it holds.
 *
 * `stepPlaceholders` accepts a placeholder in any string of a call, the id, the
 * name and the namespace included. The fill therefore walks the same strings, so
 * no placeholder that the parser accepts reaches the agent as literal braces.
 */
function fillToolCall(call: MockModelToolCall, fill: (text: string) => string): MockModelToolCall {
  return fillValue(call, fill) as MockModelToolCall
}

function fillValue(value: unknown, fill: (text: string) => string): unknown {
  if (typeof value === 'string')
    return fill(value)
  if (Array.isArray(value))
    return value.map(item => fillValue(item, fill))
  if (isRecord(value))
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, fillValue(item, fill)]))
  return value
}

function parseTextStream(value: unknown, label: string): MockModelTextStream | undefined {
  if (value === undefined)
    return undefined
  if (!isRecord(value))
    throw new Error(`Model ${label} stream must be an object`)
  if (!Number.isInteger(value.chunkChars) || Number(value.chunkChars) < 1)
    throw new Error(`Model ${label} stream chunkChars must be an integer of 1 or more`)
  const delayMs = parseDelay(value.delayMs, `${label} stream`)
  if (delayMs === undefined)
    throw new Error(`Model ${label} stream needs delayMs`)
  return { chunkChars: Number(value.chunkChars), delayMs }
}

function parseDelay(value: unknown, label: string): number | undefined {
  if (value === undefined)
    return undefined
  if (!Number.isInteger(value) || Number(value) < 0 || Number(value) > MAX_STEP_DELAY_MS)
    throw new Error(`Model ${label} delayMs must be an integer from 0 to ${MAX_STEP_DELAY_MS}`)
  return Number(value)
}

function parseUsage(value: unknown, label: string): MockModelUsage | undefined {
  if (value === undefined)
    return undefined
  if (!isRecord(value))
    throw new Error(`Model ${label} usage must be an object`)
  const usage: MockModelUsage = {}
  for (const key of ['inputTokens', 'outputTokens', 'contextWindow'] as const) {
    const raw = value[key]
    if (raw === undefined)
      continue
    if (!Number.isInteger(raw) || Number(raw) < 0)
      throw new Error(`Model ${label} usage ${key} must be a non-negative integer`)
    usage[key] = Number(raw)
  }
  return usage
}

function parseRateLimits(value: unknown, label: string): MockModelRateLimits | undefined {
  if (value === undefined)
    return undefined
  if (!isRecord(value))
    throw new Error(`Model ${label} rateLimits must be an object`)
  const type = value.type
  const status = value.status
  if (typeof type !== 'string' || !type)
    throw new Error(`Model ${label} rateLimits type must be a non-empty string`)
  if (typeof status !== 'string' || !status)
    throw new Error(`Model ${label} rateLimits status must be a non-empty string`)
  const rateLimits: MockModelRateLimits = { type, status }
  if (value.utilization !== undefined) {
    if (typeof value.utilization !== 'number' || Number(value.utilization) < 0 || Number(value.utilization) > 1)
      throw new Error(`Model ${label} rateLimits utilization must be a number from 0 to 1`)
    rateLimits.utilization = value.utilization
  }
  if (value.resetsAt !== undefined) {
    if (!Number.isInteger(value.resetsAt) || Number(value.resetsAt) < 0)
      throw new Error(`Model ${label} rateLimits resetsAt must be a non-negative integer`)
    rateLimits.resetsAt = Number(value.resetsAt)
  }
  return rateLimits
}

function parseToolCall(value: unknown, label: string, toolIndex: number): MockModelToolCall {
  if (!isRecord(value) || typeof value.id !== 'string' || !value.id || typeof value.name !== 'string' || !value.name)
    throw new Error(`Model ${label} tool call ${toolIndex} needs id and name`)
  if (value.namespace !== undefined && (typeof value.namespace !== 'string' || !value.namespace))
    throw new Error(`Model ${label} tool call ${toolIndex} namespace must be a non-empty string`)
  const hasArguments = isRecord(value.arguments)
  const hasInput = typeof value.input === 'string'
  if (hasArguments === hasInput)
    throw new Error(`Model ${label} tool call ${toolIndex} needs either object arguments or raw text input, not both`)
  return {
    id: value.id,
    name: value.name,
    ...(hasArguments ? { arguments: value.arguments as Record<string, unknown> } : {}),
    ...(hasInput ? { input: value.input as string } : {}),
    ...(typeof value.namespace === 'string' ? { namespace: value.namespace } : {}),
  }
}

function parseModelError(value: unknown, label: string): MockModelError {
  if (!isRecord(value) || !Number.isInteger(value.status) || Number(value.status) < 400 || Number(value.status) > 599 || typeof value.message !== 'string' || !value.message)
    throw new Error(`Model ${label} error needs an HTTP status and message`)
  if (value.code !== undefined && typeof value.code !== 'string')
    throw new Error(`Model ${label} error code must be a string`)
  return {
    status: Number(value.status),
    message: value.message,
    ...(typeof value.code === 'string' ? { code: value.code } : {}),
  }
}

/**
 * The scenario the request selects.
 *
 * The newest marker wins. A provider replays the complete conversation, so a
 * chat that ran two scenarios in sequence carries both markers; the last one
 * belongs to the turn the agent is answering now.
 */
export function selectScenarioID(body: unknown): string {
  const markers = collectScenarioIDs(body)
  return markers.at(-1) ?? AMBIENT_SCENARIO_ID
}

/** Every marker in the request body, in the order the body states them. */
export function collectScenarioIDs(value: unknown): string[] {
  const found: string[] = []
  visitStrings(value, (text) => {
    const pattern = new RegExp(`${SCENARIO_MARKER}([\\w-]{1,128})`, 'g')
    for (const match of text.matchAll(pattern)) {
      if (match[1])
        found.push(match[1])
    }
  })
  return found
}

/** Decide whether one request satisfies a matcher. */
export function matchesRequest(
  matcher: MockModelMatcher,
  request: { protocol: MockModelProtocol, systemText: string, userText: string, body: unknown },
): boolean {
  if (matcher.protocol && matcher.protocol !== request.protocol)
    return false
  if (!matchesPattern(matcher.system, request.systemText))
    return false
  if (!matchesPattern(matcher.user, request.userText))
    return false
  // Serialize lazily. The body can be large, and most matchers never read it.
  if (matcher.body !== undefined && !matchesPattern(matcher.body, JSON.stringify(request.body ?? null)))
    return false
  return true
}

function matchesPattern(pattern: MockModelPattern | undefined, subject: string): boolean {
  if (pattern === undefined)
    return true
  const patterns = Array.isArray(pattern) ? pattern : [pattern]
  return patterns.every(source => new RegExp(source, 'i').test(subject))
}

/**
 * The joined system text of a request.
 *
 * The three protocols each state the system prompt differently: Chat
 * Completions uses a `system` role inside `messages`, Responses uses
 * `instructions` plus a `system` or `developer` role inside `input`, and
 * Anthropic uses a top-level `system` field.
 */
export function systemText(body: unknown): string {
  if (!isRecord(body))
    return ''
  const parts: string[] = []
  if (body.system !== undefined)
    parts.push(contentText(body.system))
  if (typeof body.instructions === 'string')
    parts.push(body.instructions)
  for (const key of ['messages', 'input'] as const) {
    const items = body[key]
    if (!Array.isArray(items))
      continue
    for (const item of items) {
      if (isRecord(item) && (item.role === 'system' || item.role === 'developer'))
        parts.push(contentText(item.content))
    }
  }
  return parts.filter(Boolean).join('\n')
}

/** The text of the last user turn, across the three protocols. */
export function lastUserText(body: unknown): string {
  if (!isRecord(body))
    return ''
  for (const key of ['messages', 'input'] as const) {
    const items = body[key]
    if (!Array.isArray(items))
      continue
    for (let index = items.length - 1; index >= 0; index--) {
      const item = items[index]
      if (isRecord(item) && item.role === 'user')
        return contentText(item.content)
    }
  }
  return ''
}

/** Flatten any nested content shape into its text. */
export function contentText(value: unknown): string {
  if (typeof value === 'string')
    return value
  if (Array.isArray(value))
    return value.map(contentText).join('\n')
  if (!isRecord(value))
    return ''
  if (typeof value.text === 'string')
    return value.text
  return Object.values(value).map(contentText).join('\n')
}

function visitStrings(value: unknown, visit: (text: string) => void): void {
  if (typeof value === 'string') {
    visit(value)
    return
  }
  if (Array.isArray(value)) {
    for (const item of value)
      visitStrings(item, visit)
    return
  }
  if (isRecord(value)) {
    for (const item of Object.values(value))
      visitStrings(item, visit)
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
