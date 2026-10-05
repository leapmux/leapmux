import type { CursorRunRequestWitness } from './cursorRequestWire'
import { googleLastUserText, googlePartsText } from './googleModelContent'

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
export type MockModelProtocol = 'openai-chat-completions' | 'openai-responses' | 'anthropic-messages' | 'aws-event-stream' | 'google-generative-language'

export const MOCK_MODEL_PROTOCOLS: readonly MockModelProtocol[] = [
  'openai-chat-completions',
  'openai-responses',
  'anthropic-messages',
  'aws-event-stream',
  'google-generative-language',
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
  /** Raw text input for a custom tool in either OpenAI model API. */
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
  /** Hold a provider-service tool after its native started event. Never send this field as a tool argument. */
  completionGate?: string
  /** Native child text sent by a provider service before the task completes. */
  taskProgress?: string
  /** Request actual native client execution. The client supplies the child identity and report. */
  nativeExecution?: { modelId: string }
}

export interface MockModelError {
  /** The HTTP status of the error response. A `midStream` error answers 200 and does not send it. */
  status: number
  message: string
  code?: string
  /**
   * Send the error inside a stream that already started, instead of as an HTTP
   * status: the stream opens, one partial text delta arrives, and an error
   * payload ends it.
   *
   * Only the OpenAI Chat Completions route sends this shape, and only to a
   * request that asks for a stream. The shared model route refuses the flag in
   * every other case. The provider-service surfaces (Cursor, Kiro, Google) do
   * not read it.
   *
   * A client can treat this failure apart from an HTTP status failure.
   * qodercli 1.1.65 replaces an HTTP status error, and a stream error that
   * comes before any delta, with its own generic text. It relays the
   * provider's message of this shape in its `result`.
   */
  midStream?: boolean
}

export interface MockModelDeliveredError {
  code: string
  message: string
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
 * Reasoning chunks precede text chunks. Tool calls keep their native complete arguments.
 */
export interface MockModelTextStream {
  /** Unicode code points per piece. At least 1. */
  chunkChars: number
  /** Pause between pieces, under the same cap as `delayMs`. */
  delayMs: number
  /** Hold after each stated chunk until the test releases its gate. */
  gates?: readonly { afterChunk: number, name: string }[]
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
  /** Hold the answer until this test releases the gate. */
  gate?: string
  /** Deliver reasoning and text progressively. See `MockModelTextStream`. */
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
  let chunk = ''
  let characters = 0
  for (const character of step.text) {
    chunk += character
    characters++
    if (characters === size) {
      chunks.push(chunk)
      chunk = ''
      characters = 0
    }
  }
  if (chunk !== '')
    chunks.push(chunk)
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
  /** Match only the final message of the active generic model API. */
  lastMessage?: {
    role?: 'system' | 'developer' | 'user' | 'assistant' | 'tool'
    text?: MockModelPattern
  }
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
  /** High-priority rules precede normal rules. Declaration order stays stable within each priority. */
  priority?: 'high' | 'normal'
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

export interface MockModelServerContext {
  conversationId: string
  messages: { role: 'user' | 'assistant', content: string }[]
}

export interface MockModelCredential {
  kind: 'bearer' | 'api-key' | 'service' | 'none'
  accepted: boolean
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
  /** Completed native service history before this request. The native body stays unchanged. */
  serverContext?: MockModelServerContext
  /** The actual decoded native Run fields beside the unchanged request body. */
  nativeRequest?: CursorRunRequestWitness
  /** Actual credential validation without the credential value. */
  mockCredential?: MockModelCredential
  /** The actual allowlisted beta header. Never retain arbitrary headers or credentials. */
  requestHeaders?: { 'anthropic-beta': string }
  /** The actual completed response. Cancelled responses produce no receipt. */
  response?: { status: number, headers: Record<string, string>, serviceError?: { code: string, message: string } }
  serviceResponse?: { kind: 'amp-error-set', code: string, message: string }
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
  /** Gates that currently hold one or more model requests. */
  pendingGates: string[]
  requests: MockModelRequestRecord[]
  unexpectedRequests: MockModelUnexpectedRequest[]
}

/**
 * Limit the complete request records that one scenario retains.
 *
 * An uncapped log exhausted a 4 GB heap and stopped Playwright.
 * The V8 stack trace did not identify a test.
 * Requests can contain the conversation and every tool schema.
 * Logs of complete requests can grow quadratically with the turn count.
 *
 * The server deletes the oldest records first.
 * Failure reports include status.requests, so recent records stay available.
 */
export const MAX_SCENARIO_REQUEST_RECORDS = 500

/** State the progress of a scenario in one line, for a failure message. */
export function describeScenarioStatus(status: MockModelScenarioStatus): string {
  const unexpected = status.unexpectedRequests.length
  return `${status.nextStep} of ${status.stepCount} queued answers consumed, `
    + `${unexpected} request${unexpected === 1 ? '' : 's'} the script did not answer`
}

/**
 * Reject a step index that cannot name an ordered step.
 * The index and the step count after it (`stepIndex + 1`) must both be safe integers.
 */
export function validateStepIndex(stepIndex: number): void {
  if (!Number.isSafeInteger(stepIndex) || stepIndex < 0 || !Number.isSafeInteger(stepIndex + 1))
    throw new Error(`A model script step index must be a nonnegative safe integer, not ${stepIndex}.`)
}

/**
 * Return the request that consumed the ordered step `stepIndex`.
 *
 * A status that holds no such request fails with one message that states the cause and the script state:
 *
 * - The agent did not request the step yet.
 * - The agent requested the step, but the server dropped its record at the {@link MAX_SCENARIO_REQUEST_RECORDS} cap.
 *
 * A caller that must wait for the step calls `ModelScript.requestAt` instead.
 */
export function stepRequest(status: MockModelScenarioStatus, stepIndex: number): MockModelRequestRecord {
  validateStepIndex(stepIndex)
  const request = status.requests.find(record => record.stepIndex === stepIndex)
  if (request)
    return request
  const cause = stepIndex < status.nextStep
    ? `the agent requested it, but the server keeps only the newest ${MAX_SCENARIO_REQUEST_RECORDS} request records`
    : 'the agent did not request it'
  throw new Error(`The model script holds no request for step ${stepIndex}: ${cause}; ${describeScenarioStatus(status)}.`)
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
  if (value.priority !== undefined && value.priority !== 'high' && value.priority !== 'normal')
    throw new Error(`Model rule ${value.name} priority must be high or normal`)
  return {
    name: value.name,
    when: parseMatcher(value.when, value.name),
    respond: parseStep(value.respond, `rule ${value.name}`),
    ...(value.priority === undefined ? {} : { priority: value.priority }),
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
    matcher[field] = parseModelPattern(declared, `Model rule ${ruleName} ${field}`)
  }
  if (value.lastMessage !== undefined) {
    const message = value.lastMessage
    if (!isRecord(message) || Object.keys(message).length === 0 || Object.keys(message).some(key => key !== 'role' && key !== 'text'))
      throw new Error(`Model rule ${ruleName} lastMessage must contain role or text criteria only`)
    const role = (['system', 'developer', 'user', 'assistant', 'tool'] as const).find(candidate => candidate === message.role)
    if (message.role !== undefined && role === undefined)
      throw new Error(`Model rule ${ruleName} lastMessage role is invalid`)
    if (role === undefined && message.text === undefined)
      throw new Error(`Model rule ${ruleName} lastMessage needs role or text`)
    matcher.lastMessage = {
      ...(role === undefined ? {} : { role }),
      ...(message.text === undefined ? {} : { text: parseModelPattern(message.text, `Model rule ${ruleName} lastMessage text`) }),
    }
  }
  return matcher
}

function parseModelPattern(value: unknown, label: string): MockModelPattern {
  const declared: unknown[] = Array.isArray(value) ? value : [value]
  if (declared.length === 0)
    throw new Error(`${label} must state at least one pattern`)
  const patterns = declared.map((pattern) => {
    if (typeof pattern !== 'string' || !pattern)
      throw new Error(`${label} must be a non-empty pattern`)
    try {
      void new RegExp(pattern, 'i')
    }
    catch (error) {
      throw new Error(`${label} is not a valid regular expression`, { cause: error })
    }
    return pattern
  })
  return Array.isArray(value) ? patterns : patterns[0]!
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
  if (value.text === undefined && value.reasoning === undefined && toolCalls === undefined && error === undefined)
    throw new Error(`Model ${label} needs text, reasoning, toolCalls, or error`)
  if (error && (value.text !== undefined || toolCalls !== undefined || value.reasoning !== undefined))
    throw new Error(`Model ${label} cannot combine an error with output`)
  const delayMs = parseDelay(value.delayMs, label)
  const gate = parseGate(value.gate, label)
  if (gate !== undefined && delayMs !== undefined)
    throw new Error(`Model ${label} cannot combine gate with delayMs`)
  const stream = parseTextStream(value.stream, label)
  if (stream && value.text === undefined && value.reasoning === undefined)
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
    ...(gate === undefined ? {} : { gate }),
    ...(stream === undefined ? {} : { stream }),
    ...(usage === undefined ? {} : { usage }),
    ...(rateLimits === undefined ? {} : { rateLimits }),
    ...(captures === undefined ? {} : { captures }),
  }
  if (!captures)
    validateStreamGatePositions(step, label)
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

function parseGate(value: unknown, label: string): string | undefined {
  if (value === undefined)
    return undefined
  validateGateName(value, `Model ${label} gate`)
  return value
}

export function validateGateName(value: unknown, label = 'Model gate'): asserts value is string {
  if (typeof value !== 'string' || !/^[a-z][\w-]{0,63}$/i.test(value))
    throw new Error(`${label} must use 1 to 64 ASCII letters, digits, underscores, or hyphens and start with a letter`)
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
  validateStreamGatePositions(resolved, 'resolved step')
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
  if (value.gates === undefined)
    return { chunkChars: Number(value.chunkChars), delayMs }
  if (!Array.isArray(value.gates))
    throw new Error(`Model ${label} stream gates must be an array`)
  const names = new Set<string>()
  let previous = 0
  const gates = value.gates.map((gate: unknown) => {
    if (!isRecord(gate) || !Number.isSafeInteger(gate.afterChunk) || Number(gate.afterChunk) <= previous)
      throw new Error(`Model ${label} stream gate positions must be positive, strictly increasing safe integers`)
    validateGateName(gate.name, `Model ${label} stream gate`)
    if (names.has(gate.name))
      throw new Error(`Model ${label} stream gate ${gate.name} is declared twice`)
    names.add(gate.name)
    previous = Number(gate.afterChunk)
    return { afterChunk: previous, name: gate.name }
  })
  return { chunkChars: Number(value.chunkChars), delayMs, gates }
}

function validateStreamGatePositions(step: MockModelStep, label: string): void {
  const last = step.stream?.gates?.at(-1)
  if (!last)
    return
  const count = textChunks(step).length + textChunks({ ...(step.reasoning === undefined ? {} : { text: step.reasoning }), stream: step.stream! }).length
  if (last.afterChunk > count)
    throw new Error(`Model ${label} stream gate ${last.name} exceeds the emitted chunk count ${count}`)
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
    if (typeof value.utilization !== 'number' || !Number.isFinite(value.utilization) || value.utilization < 0 || value.utilization > 1)
      throw new Error(`Model ${label} rateLimits utilization must be a number from 0 to 1`)
    rateLimits.utilization = value.utilization
  }
  if (value.resetsAt !== undefined) {
    if (!Number.isSafeInteger(value.resetsAt) || Number(value.resetsAt) < 0)
      throw new Error(`Model ${label} rateLimits resetsAt must be a non-negative integer`)
    if (Number(value.resetsAt) > 253_402_300_799)
      throw new Error(`Model ${label} rateLimits resetsAt must fit a UTC date through the year 9999`)
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
  const completionGate = parseGate(value.completionGate, `${label} tool call ${toolIndex} completion`)
  if (value.taskProgress !== undefined && (typeof value.taskProgress !== 'string' || value.taskProgress.length === 0))
    throw new Error(`Model ${label} tool call ${toolIndex} taskProgress must be a non-empty string`)
  if (value.nativeExecution !== undefined && (!isRecord(value.nativeExecution) || typeof value.nativeExecution.modelId !== 'string' || value.nativeExecution.modelId.length === 0 || Object.keys(value.nativeExecution).some(key => key !== 'modelId')))
    throw new Error(`Model ${label} tool call ${toolIndex} nativeExecution requires only a non-empty modelId`)
  return {
    id: value.id,
    name: value.name,
    ...(hasArguments ? { arguments: value.arguments as Record<string, unknown> } : {}),
    ...(hasInput ? { input: value.input as string } : {}),
    ...(typeof value.namespace === 'string' ? { namespace: value.namespace } : {}),
    ...(completionGate === undefined ? {} : { completionGate }),
    ...(typeof value.taskProgress === 'string' ? { taskProgress: value.taskProgress } : {}),
    ...(isRecord(value.nativeExecution) && typeof value.nativeExecution.modelId === 'string' ? { nativeExecution: { modelId: value.nativeExecution.modelId } } : {}),
  }
}

function parseModelError(value: unknown, label: string): MockModelError {
  if (!isRecord(value) || !Number.isInteger(value.status) || Number(value.status) < 400 || Number(value.status) > 599 || typeof value.message !== 'string' || !value.message)
    throw new Error(`Model ${label} error needs an HTTP status and message`)
  if (value.code !== undefined && typeof value.code !== 'string')
    throw new Error(`Model ${label} error code must be a string`)
  if (value.midStream !== undefined && typeof value.midStream !== 'boolean')
    throw new Error(`Model ${label} error midStream must be a boolean`)
  return {
    status: Number(value.status),
    message: value.message,
    ...(typeof value.code === 'string' ? { code: value.code } : {}),
    ...(value.midStream === true ? { midStream: true } : {}),
  }
}

/**
 * The scenario the request selects.
 *
 * The newest marker in actual user text wins.
 * Replayed conversations can contain several prompt markers.
 * Native session metadata and tool replies cannot select the scenario.
 *
 * The system text selects the scenario only when no user text holds a marker.
 * A native summary request can quote the conversation in its system prompt
 * and send a fixed instruction as its only user text. Goose 1.53.0 does this
 * for `/compact`, so its user text never holds the marker of the prompts that
 * it summarizes.
 */
export function selectScenarioID(body: unknown): string {
  return collectScenarioIDs(body).at(-1) ?? scenarioMarkers(systemText(body)).at(-1) ?? AMBIENT_SCENARIO_ID
}

/** Read markers from user prompts in their conversation order. */
export function collectScenarioIDs(value: unknown): string[] {
  return scenarioUserTexts(value).flatMap(text => scenarioMarkers(text))
}

/** Read the markers of one text in their order. */
function scenarioMarkers(text: string): string[] {
  const pattern = new RegExp(`${SCENARIO_MARKER}([\\w-]{1,128})`, 'g')
  return [...text.matchAll(pattern)].flatMap(match => match[1] ? [match[1]] : [])
}

function scenarioUserTexts(body: unknown): string[] {
  if (typeof body === 'string')
    return [body]
  if (!isRecord(body))
    return []
  if (typeof body.input === 'string')
    return [body.input]
  if (Array.isArray(body.contents)) {
    return body.contents.filter(isRecord).filter(message => message.role === 'user').map(message => googlePartsText(message.parts))
  }
  const rows = Array.isArray(body.messages) ? body.messages : Array.isArray(body.input) ? body.input : []
  const prompts: string[] = []
  for (const message of rows) {
    if (!isRecord(message) || message.role !== 'user')
      continue
    if (typeof message.content === 'string') {
      prompts.push(message.content)
    }
    else if (Array.isArray(message.content)) {
      for (const block of message.content) {
        if (isRecord(block) && (block.type === 'text' || block.type === 'input_text') && typeof block.text === 'string')
          prompts.push(block.text)
      }
    }
  }
  return prompts
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
  if (matcher.lastMessage !== undefined) {
    if (request.protocol === 'aws-event-stream' || !isRecord(request.body))
      return false
    const rows = request.protocol === 'google-generative-language' ? request.body.contents : request.protocol === 'openai-responses' ? request.body.input : request.body.messages
    const message: unknown = Array.isArray(rows) ? rows.at(-1) : undefined
    if (!isRecord(message) || (matcher.lastMessage.role !== undefined && matcher.lastMessage.role !== message.role))
      return false
    if (!matchesPattern(matcher.lastMessage.text, request.protocol === 'google-generative-language' ? googlePartsText(message.parts) : contentText(message.content)))
      return false
  }
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
  if (isRecord(body.systemInstruction))
    parts.push(googlePartsText(body.systemInstruction.parts))
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

/** Read the last user prompt across the supported model APIs. */
export function lastUserText(body: unknown): string {
  if (!isRecord(body))
    return ''
  if (Array.isArray(body.contents))
    return googleLastUserText(body.contents)
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

/** Visit strings for explicit body matchers and capture substitutions. */
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
