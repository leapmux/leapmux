import type { IncomingMessage, ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import type { Duplex } from 'node:stream'
import type {
  MockModelError,
  MockModelProtocol,
  MockModelRequestRecord,
  MockModelRule,
  MockModelScenarioSpec,
  MockModelScenarioStatus,
  MockModelStep,
  MockModelUnexpectedRequest,
} from './mockModelScript'
import { Buffer } from 'node:buffer'
import { createServer } from 'node:http'
import { createServer as createHttp2Server } from 'node:http2'
import { AMP_ACTOR_PATH_PREFIX, createAmpSurface, isAmpPath } from './ampSurface'
import { answerCursorStartup, CURSOR_RUN_PATH, cursorTaskCallsFrom, isCursorPath, serveCursorRun } from './cursorSurface'
import { createDualVersionListener } from './dualVersionListener'
import { isKiroRequest, kiroOperation, kiroSystemText, kiroUserText, serveKiro } from './kiroSurface'
import {
  isRecord,
  lastUserText,
  matchesRequest,
  parseScenarioSpec,
  resolveStepCaptures,
  selectScenarioID,
  systemText,
  textChunks,
  validateScenarioID,
} from './mockModelScript'
import { pauseBetweenChunks } from './responsePause'

const MAX_REQUEST_BYTES = 16 * 1024 * 1024
const MAX_HTTP_REQUEST_RECORDS = 10_000
const MAX_UNMATCHED_RECORDS = 200

/**
 * How many requests ONE scenario keeps, body and all.
 *
 * Every other collection here was capped and this one was not, which is what
 * turned a runaway scenario into a 4 GB heap exhaustion that killed the whole
 * Playwright runner with a V8 stack trace naming no test. A provider request
 * body carries the entire conversation plus every tool schema -- tens of
 * kilobytes that GROW each turn -- so an unbounded log is quadratic in the turn
 * count.
 *
 * The OLDEST records go first: a diagnosis reads the end of a run, not its
 * start, and `status.requests` is what a failure attaches.
 */
const MAX_SCENARIO_REQUEST_RECORDS = 500

/**
 * How many requests one scenario's FALLBACK may answer.
 *
 * A fallback exists for a turn count a test cannot predict, which is not the
 * same as an unbounded one. Several providers start turns of their OWN -- Codex
 * runs one after another while a session goal is active -- so a fallback that
 * always answers is an infinite loop running at mock speed, and the first
 * symptom is the runner dying rather than the test failing.
 *
 * Past this, the scenario answers no more: the request is recorded as
 * unexpected and the turn fails, which names the loop where it happens.
 */
const MAX_FALLBACK_ANSWERS = 200

export interface MockModelHTTPRequestRecord {
  method: string
  path: string
  /**
   * The operation of a call of Kiro's service. Every such call is `POST /`, and the
   * operation is in a header, so the path alone cannot tell two calls apart.
   */
  operation?: string
  status: number
}

/** A model request that reached no scenario at all. */
export interface MockModelUnmatchedRequest {
  protocol: MockModelProtocol
  path: string
  scenarioID: string
  reason: string
  body: unknown
}

export interface MockModelRequestLog {
  http: MockModelHTTPRequestRecord[]
  unmatched: MockModelUnmatchedRequest[]
}

export interface MockModelServerOptions {
  /** The model identifiers the catalog routes advertise. */
  models: readonly string[]
}

export interface MockModelServer {
  url: string
  close: () => Promise<void>
  /**
   * How many requests the proxy refused, for each host: each tunnel (`CONNECT`,
   * keyed by its `host:port`) and each absolute-form request to a host that is not
   * the mock. The suite reports them at its end, so a host that an agent tries to
   * reach is visible and not only refused.
   */
  refusedHosts: () => ReadonlyMap<string, number>
}

/** Whether a request target is in absolute form (`http://host/path`), as a proxy receives it. */
function isAbsoluteForm(target: string): boolean {
  return /^[a-z][\w+.-]*:\/\//i.test(target)
}

/** The host names that reach the mock itself. */
const LOOPBACK_HOSTNAMES: ReadonlySet<string> = new Set(['127.0.0.1', 'localhost', '[::1]'])

/** Whether an absolute-form target addresses the mock itself. */
function isOwnOrigin(target: URL, ownPort: number): boolean {
  return LOOPBACK_HOSTNAMES.has(target.hostname) && Number(target.port) === ownPort
}

interface ScenarioState {
  spec: MockModelScenarioSpec
  nextStep: number
  /** How many requests the fallback answered; see MAX_FALLBACK_ANSWERS. */
  fallbackAnswers: number
  ruleMatches: Map<string, number>
  requests: MockModelRequestRecord[]
  unexpectedRequests: MockModelUnexpectedRequest[]
}

interface ModelRequestContext {
  protocol: MockModelProtocol
  path: string
  body: unknown
  systemText: string
  userText: string
}

/** The step that answers one model request, or the reason that no step answers it. */
type ScenarioAnswer
  = | { kind: 'step', step: MockModelStep }
    | { kind: 'missing', message: string }

/**
 * Start a strict model server for every model surface that the coding agents use:
 * three model APIs, and the own services of Cursor, Kiro and Amp.
 *
 * The server holds no prompt knowledge. Every answer comes from a script that a
 * test registered, so a provider that changes its prompts cannot change what a
 * test observes. See `./mockModelScript` for the vocabulary and
 * `./mockModelScenario` for the client that registers one.
 */
export async function createMockModelServer(options: MockModelServerOptions): Promise<MockModelServer> {
  if (options.models.length === 0)
    throw new Error('The mock model server needs at least one model identifier')
  const models = [...options.models]
  const scenarios = new Map<string, ScenarioState>()
  const http: MockModelHTTPRequestRecord[] = []
  const unmatched: MockModelUnmatchedRequest[] = []
  const refusedHosts = new Map<string, number>()
  let responseSequence = 0
  // The port the server listens on. It is set before the server accepts a request.
  let ownPort = 0

  // Record one request that the proxy refused, and count its host.
  const refuse = (host: string, record: MockModelHTTPRequestRecord): void => {
    http.push(record)
    if (http.length > MAX_HTTP_REQUEST_RECORDS)
      http.shift()
    refusedHosts.set(host, (refusedHosts.get(host) ?? 0) + 1)
  }

  // Every surface asks here, so each one records a request of a scenario that no
  // test registered in the same way, and states the same reason.
  const answerFor = (context: ModelRequestContext): ScenarioAnswer => {
    const scenarioID = selectScenarioID(context.body)
    const scenario = scenarios.get(scenarioID)
    if (!scenario) {
      recordUnmatched(unmatched, { ...context, scenarioID, reason: 'the scenario is not registered' })
      return { kind: 'missing', message: `The model scenario ${scenarioID} is not registered` }
    }
    const step = selectStep(scenario, context)
    if (!step)
      return { kind: 'missing', message: `The model scenario ${scenarioID} has no answer for this request. Its status lists the reason.` }
    return { kind: 'step', step }
  }

  // Amp's own service, which runs the agent loop and asks the scenarios for each
  // inference; see `./ampSurface`.
  const amp = createAmpSurface({
    answer: async (inference) => {
      const context: ModelRequestContext = {
        // The loop states its conversation in the Anthropic Messages shape, so a
        // matcher reads it the way it reads a Claude request.
        protocol: 'anthropic-messages',
        path: AMP_ACTOR_PATH_PREFIX,
        body: inference.body,
        systemText: '',
        userText: inference.userText,
      }
      const answer = answerFor(context)
      return answer.kind === 'step' ? answer.step : undefined
    },
  })

  const handle = async (request: IncomingMessage, response: ServerResponse): Promise<void> => {
    try {
      // A request that a client sends through the proxy arrives in absolute form.
      // One to another host is refused, as a tunnel is: answering it by its path
      // would serve a real host's request as a model route. One to the mock itself
      // is served as if it came directly.
      const target = request.url ?? '/'
      if (isAbsoluteForm(target)) {
        const absolute = new URL(target)
        if (!isOwnOrigin(absolute, ownPort)) {
          refuse(absolute.host, { method: request.method ?? 'UNKNOWN', path: target, status: 403 })
          response.writeHead(403, { 'content-length': '0', 'connection': 'close' }).end()
          return
        }
      }
      const url = new URL(target, 'http://127.0.0.1')
      if (url.pathname === '/__e2e/requests') {
        handleRequestLog(request, response, { http, unmatched })
        return
      }
      if (!url.pathname.startsWith('/__e2e/'))
        recordHTTPRequest(http, request, response, url)
      if (url.pathname === '/healthz') {
        writeJSON(response, 200, { ok: true })
        return
      }
      if (url.pathname.startsWith('/__e2e/scenarios/')) {
        await handleScenarioControl(request, response, url, scenarios)
        return
      }
      if (request.method === 'GET' && isModelsPath(url.pathname)) {
        writeJSON(response, 200, modelCatalog(models))
        return
      }
      if (handleIdentityRoute(request, response, url))
        return

      // Qoder CLI's auth and catalog routes. The CLI reaches them at the origin
      // its `QODER_CENTER_ENDPOINT` names, which is the mock itself.
      if (handleQoderAuthRoute(request, response, url, ownPort))
        return

      // Amp's own service, which is not a model API either.
      if (isAmpPath(url.pathname)) {
        await amp.handleHttp(request, response, url)
        return
      }

      // Cursor's own backend, which is not a model API at all. Its startup
      // calls take an all-defaults answer apart from the model catalogue, and
      // its turn arrives on one bidirectional stream; see `./cursorSurface`.
      if (isCursorPath(url.pathname)) {
        if (url.pathname === CURSOR_RUN_PATH) {
          await serveCursorRun(request, response, {
            answer: async (prompt) => {
              const context: ModelRequestContext = {
                // The stream carries protobuf, not JSON. `openai-responses` is
                // the nearest protocol a matcher can name, and a Cursor test
                // matches on `user` or `body` rather than on the protocol.
                protocol: 'openai-responses',
                path: url.pathname,
                body: { prompt },
                systemText: '',
                userText: prompt,
              }
              const answer = answerFor(context)
              if (answer.kind !== 'step')
                return undefined
              const { step } = answer
              if (step.delayMs)
                await new Promise<void>(resolve => setTimeout(resolve, step.delayMs))
              const taskCalls = cursorTaskCallsFrom(step.toolCalls)
              if (step.text === undefined && taskCalls.length === 0)
                return undefined
              return { text: step.text, taskCalls }
            },
          })
          return
        }
        answerCursorStartup(request, response, url.pathname)
        return
      }

      // Kiro's own service, which states its operation in a header on `POST /`
      // and answers a turn with an AWS event stream. See `./kiroSurface`.
      if (isKiroRequest(request)) {
        const body = await readJSONBody(request)
        await serveKiro(request, response, body, {
          answer: async (kiroBody) => {
            const context: ModelRequestContext = {
              protocol: 'aws-event-stream',
              path: url.pathname,
              body: kiroBody,
              systemText: kiroSystemText(kiroBody),
              userText: kiroUserText(kiroBody),
            }
            const answer = answerFor(context)
            if (answer.kind === 'step' && answer.step.delayMs && !await holdOpen(request, response, answer.step.delayMs))
              return { kind: 'abandoned' }
            return answer
          },
        })
        return
      }

      const protocol = protocolFor(request.method, url.pathname)
      if (!protocol) {
        writeJSON(response, 404, { error: { message: `No mock route for ${request.method ?? 'UNKNOWN'} ${url.pathname}` } })
        return
      }
      const body = await readJSONBody(request)
      const context: ModelRequestContext = {
        protocol,
        path: url.pathname,
        body,
        systemText: systemText(body),
        userText: lastUserText(body),
      }
      const answer = answerFor(context)
      if (answer.kind === 'missing') {
        writeJSON(response, 409, { error: { message: answer.message } })
        return
      }
      const { step } = answer
      if (step.delayMs && !await holdOpen(request, response, step.delayMs))
        return
      if (step.error) {
        writeModelError(response, protocol, step.error)
        return
      }
      responseSequence++
      await writeModelResponse(response, protocol, body, step, `mock-response-${responseSequence}`)
    }
    catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (!response.headersSent)
        writeJSON(response, 400, { error: { message } })
      else
        response.destroy(error instanceof Error ? error : new Error(message))
    }
  }

  // TWO servers behind ONE port. Every provider but Cursor speaks HTTP/1.1;
  // Cursor's `agent.v1.AgentService/Run` is HTTP/2, and its startup calls are
  // HTTP/1.1 to the same endpoint. See `./dualVersionListener` for why Node
  // cannot serve both from one server.
  const http1 = createServer(handle)
  // The mock is also the HTTPS proxy of the `leapmux dev` process, and so of the
  // hub, the worker, the terminal shells and every agent. It refuses every tunnel,
  // and `handle` refuses every absolute-form request to another host. A request
  // that no setting points at the mock -- a telemetry upload, an update check, a
  // remote feature -- then fails at once instead of leaving the machine.
  http1.on('connect', (request: IncomingMessage, socket: Duplex) => {
    const host = request.url ?? ''
    refuse(host, { method: 'CONNECT', path: host, status: 403 })
    socket.on('error', () => socket.destroy())
    socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
  })
  const http2 = createHttp2Server(handle as never)
  const { server } = createDualVersionListener(http1, http2)
  // Amp's CLI reaches its thread actor over a WebSocket. An upgraded socket leaves the
  // HTTP server's own tracking, so the close below ends each one itself.
  const upgraded = new Set<Duplex>()
  http1.on('upgrade', (request: IncomingMessage, socket: Duplex, head: Buffer) => {
    upgraded.add(socket)
    socket.once('close', () => upgraded.delete(socket))
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname.startsWith(AMP_ACTOR_PATH_PREFIX))
      amp.handleUpgrade(request, socket, head, url)
    else
      socket.end('HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n')
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', reject)
      resolve()
    })
  })
  const { port } = server.address() as AddressInfo
  ownPort = port
  return {
    url: `http://127.0.0.1:${port}`,
    refusedHosts: () => new Map(refusedHosts),
    close: () => new Promise<void>((resolve, reject) => {
      amp.close()
      server.close(error => error ? reject(error) : resolve())
      http1.closeAllConnections()
      for (const socket of upgraded)
        socket.destroy()
    }),
  }
}

/**
 * Choose the answer for one request, with its captures resolved.
 *
 * A step whose capture matches nothing in the request records the request as
 * unexpected and returns nothing, exactly as an exhausted queue does.
 */
function selectStep(scenario: ScenarioState, context: ModelRequestContext): MockModelStep | undefined {
  const step = chooseStep(scenario, context)
  if (!step)
    return undefined
  const resolved = resolveStepCaptures(step, context.body)
  if ('step' in resolved)
    return resolved.step
  scenario.unexpectedRequests.push({
    protocol: context.protocol,
    path: context.path,
    reason: `capture ${resolved.unmatchedCapture} matched nothing in the request`,
    body: context.body,
  })
  return undefined
}

/**
 * Choose the answer for one request.
 *
 * A rule wins over the queue, so a provider's own housekeeping turn cannot take
 * the step the test scripted for the next real turn. An exhausted queue records
 * the request and returns nothing, which makes the scenario incomplete.
 */
function chooseStep(scenario: ScenarioState, context: ModelRequestContext): MockModelStep | undefined {
  const rule = findRule(scenario, context)
  if (rule) {
    scenario.ruleMatches.set(rule.name, (scenario.ruleMatches.get(rule.name) ?? 0) + 1)
    recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, rule: rule.name, body: context.body })
    return rule.respond
  }
  if (scenario.nextStep >= scenario.spec.steps.length) {
    const { fallback } = scenario.spec
    if (fallback && scenario.fallbackAnswers < MAX_FALLBACK_ANSWERS) {
      scenario.fallbackAnswers++
      recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, fallback: true, body: context.body })
      return fallback
    }
    if (fallback) {
      scenario.unexpectedRequests.push({
        protocol: context.protocol,
        path: context.path,
        reason: `the fallback answered ${MAX_FALLBACK_ANSWERS} times, which is a turn loop rather than an unpredictable turn count`,
        body: context.body,
      })
      return undefined
    }
    scenario.unexpectedRequests.push({
      protocol: context.protocol,
      path: context.path,
      reason: 'scenario exhausted',
      body: context.body,
    })
    return undefined
  }
  const stepIndex = scenario.nextStep++
  recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, stepIndex, body: context.body })
  return scenario.spec.steps[stepIndex]
}

/** Record one answered request, dropping the oldest once the cap is reached. */
function recordScenarioRequest(scenario: ScenarioState, record: MockModelRequestRecord): void {
  scenario.requests.push(record)
  if (scenario.requests.length > MAX_SCENARIO_REQUEST_RECORDS)
    scenario.requests.splice(0, scenario.requests.length - MAX_SCENARIO_REQUEST_RECORDS)
}

function findRule(scenario: ScenarioState, context: ModelRequestContext): MockModelRule | undefined {
  return scenario.spec.rules.find((rule) => {
    if (rule.once && (scenario.ruleMatches.get(rule.name) ?? 0) > 0)
      return false
    return matchesRequest(rule.when, context)
  })
}

/**
 * Keep the response open for the scripted delay.
 *
 * Returns false when the client gave up first. An interrupt test cancels the
 * turn inside this window, and the agent closes the socket; writing a response
 * after that point destroys the connection instead.
 *
 * Watches the RESPONSE as well as the request. `IncomingMessage` emits `close`
 * once the message completes, which this code has already done by reading the
 * body — so on a runtime that emits it at that moment rather than at socket
 * close, the request alone would report a disconnect that never happened, or
 * report none at all. `ServerResponse` emits `close` when the exchange ends for
 * any reason, which is the signal that holds on every runtime.
 */
function holdOpen(request: IncomingMessage, response: ServerResponse, delayMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout>
    const onDisconnect = () => finish(false)
    function finish(delivered: boolean) {
      if (settled)
        return
      settled = true
      clearTimeout(timer)
      request.off('aborted', onDisconnect)
      response.off('close', onDisconnect)
      resolve(delivered)
    }
    timer = setTimeout(finish, delayMs, true)
    request.once('aborted', onDisconnect)
    response.once('close', onDisconnect)
  })
}

function handleRequestLog(request: IncomingMessage, response: ServerResponse, log: MockModelRequestLog): void {
  if (request.method === 'GET') {
    writeJSON(response, 200, log)
    return
  }
  if (request.method === 'DELETE') {
    log.http.length = 0
    log.unmatched.length = 0
    response.writeHead(204).end()
    return
  }
  writeJSON(response, 405, { error: { message: `Method ${request.method ?? 'UNKNOWN'} is not allowed` } })
}

function recordHTTPRequest(
  http: MockModelHTTPRequestRecord[],
  request: IncomingMessage,
  response: ServerResponse,
  url: URL,
): void {
  const record: MockModelHTTPRequestRecord = {
    method: request.method ?? 'UNKNOWN',
    path: url.pathname,
    ...(isKiroRequest(request) ? { operation: kiroOperation(request) } : {}),
    status: 0,
  }
  http.push(record)
  if (http.length > MAX_HTTP_REQUEST_RECORDS)
    http.shift()
  response.once('finish', () => {
    record.status = response.statusCode
  })
}

function recordUnmatched(unmatched: MockModelUnmatchedRequest[], record: MockModelUnmatchedRequest): void {
  unmatched.push(record)
  if (unmatched.length > MAX_UNMATCHED_RECORDS)
    unmatched.shift()
}

async function handleScenarioControl(
  request: IncomingMessage,
  response: ServerResponse,
  url: URL,
  scenarios: Map<string, ScenarioState>,
): Promise<void> {
  const id = decodeURIComponent(url.pathname.slice('/__e2e/scenarios/'.length))
  validateScenarioID(id)
  if (request.method === 'PUT') {
    if (scenarios.has(id)) {
      writeJSON(response, 409, { error: { message: `The model scenario ${id} already exists` } })
      return
    }
    const spec = parseScenarioSpec(await readJSONBody(request))
    const state: ScenarioState = { spec, nextStep: 0, fallbackAnswers: 0, ruleMatches: new Map(), requests: [], unexpectedRequests: [] }
    scenarios.set(id, state)
    writeJSON(response, 201, scenarioStatus(state))
    return
  }

  const scenario = scenarios.get(id)
  if (!scenario) {
    writeJSON(response, 404, { error: { message: `The model scenario ${id} does not exist` } })
    return
  }
  if (request.method === 'GET') {
    writeJSON(response, 200, scenarioStatus(scenario))
    return
  }
  if (request.method === 'POST') {
    extendScenario(scenario, parseScenarioSpec(await readJSONBody(request)))
    writeJSON(response, 200, scenarioStatus(scenario))
    return
  }
  if (request.method === 'DELETE') {
    const status = scenarioStatus(scenario)
    if (!status.complete && url.searchParams.get('force') !== 'true') {
      writeJSON(response, 409, status)
      return
    }
    scenarios.delete(id)
    response.writeHead(204).end()
    return
  }
  writeJSON(response, 405, { error: { message: `Method ${request.method ?? 'UNKNOWN'} is not allowed` } })
}

/**
 * Add to a live script.
 *
 * A step joins the end of the queue, because the queue is ordered. A rule joins
 * the FRONT, because the server takes the first match: a rule a test adds later
 * must win over one the registration installed, and the housekeeping rules are
 * installed at registration.
 */
function extendScenario(scenario: ScenarioState, addition: MockModelScenarioSpec): void {
  const names = new Set(scenario.spec.rules.map(rule => rule.name))
  for (const rule of addition.rules) {
    if (names.has(rule.name))
      throw new Error(`Model rule ${rule.name} is declared twice`)
    names.add(rule.name)
  }
  scenario.spec = {
    steps: [...scenario.spec.steps, ...addition.steps],
    rules: [...addition.rules, ...scenario.spec.rules],
    // A later fallback replaces an earlier one: a test states one answer for
    // everything past its queue, not a chain of them.
    ...(addition.fallback ?? scenario.spec.fallback ? { fallback: addition.fallback ?? scenario.spec.fallback } : {}),
  }
}

function scenarioStatus(scenario: ScenarioState): MockModelScenarioStatus {
  return {
    complete: scenario.nextStep === scenario.spec.steps.length && scenario.unexpectedRequests.length === 0,
    nextStep: scenario.nextStep,
    stepCount: scenario.spec.steps.length,
    ruleMatches: Object.fromEntries(scenario.ruleMatches),
    requests: scenario.requests,
    unexpectedRequests: scenario.unexpectedRequests,
  }
}

/**
 * Identity and account routes.
 *
 * GitHub Copilot and Cursor both call these before their first model request.
 * They carry no prompt, so they answer from a fixed shape rather than a script.
 */
function handleIdentityRoute(request: IncomingMessage, response: ServerResponse, url: URL): boolean {
  if (request.method === 'GET' && url.pathname === '/copilot_internal/user') {
    const requestOrigin = `http://${request.headers.host ?? '127.0.0.1'}`
    writeJSON(response, 200, {
      login: 'leapmux-e2e',
      copilot_plan: 'individual_pro',
      token_based_billing: false,
      is_mcp_enabled: false,
      endpoints: { api: requestOrigin, telemetry: requestOrigin },
      analytics_tracking_id: 'leapmux-e2e',
    })
    return true
  }
  if (request.method === 'GET' && url.pathname === '/user') {
    writeJSON(response, 200, { id: 1, login: 'leapmux-e2e', name: 'LeapMux E2E', type: 'User', site_admin: false })
    return true
  }
  if (request.method === 'POST' && url.pathname === '/auto') {
    writeJSON(response, 200, {
      session_token: 'leapmux-e2e-session-token',
      selected_model: { id: 'gpt-5.6-luna', name: 'gpt-5.6-luna', capabilities: modelCapabilities() },
    })
    return true
  }
  return false
}

/**
 * Qoder CLI's own auth and catalog routes, served from the mock.
 *
 * The headless auth gate blocks stream-json until the account is authenticated.
 * The E2E recipe mocks authentication the way Cursor and Copilot do: the
 * `QODER_CENTER_ENDPOINT` seam points Qoder's `center` API at this mock, and the
 * SDK access-token payload (`QODER_SDK_AUTH_PAYLOAD_FILE`) installs a fake
 * credential that the mock accepts. The routes below are the calls the CLI makes
 * before its first model request; each carries no prompt, so it answers from a
 * fixed shape. See `.tmp/impl/reports/codebuddy-qoder.md` for the full recipe.
 */
function handleQoderAuthRoute(request: IncomingMessage, response: ServerResponse, url: URL, ownPort: number): boolean {
  const origin = `http://127.0.0.1:${ownPort}`
  if (url.pathname === '/algo/api/v3/service/region/endpoints' || url.pathname === '/algo/api/v5/service/region/endpoints') {
    // The endpoint-election parser reads `centerNodes`/`inferNodes`/`security`/
    // `openapiNodes` off the top level and elects each origin it lists. Listing
    // the mock's own origin routes every later call -- the token exchange, the
    // userinfo lookup and the model call -- back here.
    const body = {
      centerNodes: [origin],
      inferNodes: [origin],
      security: [origin],
      openapiNodes: [origin],
    }
    console.error('[qoder-auth] region/endpoints ->', JSON.stringify(body))
    writeJSON(response, 200, body)
    return true
  }
  if (request.method === 'POST' && (url.pathname === '/api/v1/jobToken/exchange' || url.pathname === '/api/v1/jobToken/refresh')) {
    // `identity.job.exchange` reads `token` (or `access_token`) off the body and
    // installs it. A far-future expiry keeps the refresh path quiet.
    writeJSON(response, 200, {
      token: 'leapmux-e2e-token',
      access_token: 'leapmux-e2e-token',
      refresh_token: 'leapmux-e2e-token',
      expires_at: Date.now() + 86_400_000,
      refresh_token_expires_at: Date.now() + 86_400_000 * 30,
    })
    return true
  }
  if (request.method === 'GET' && url.pathname === '/api/v1/userinfo') {
    writeJSON(response, 200, { id: 'leapmux-e2e', uid: 'leapmux-e2e', username: 'leapmux-e2e', name: 'LeapMux E2E', email: 'e2e@leapmux.test' })
    return true
  }
  if (url.pathname === '/api/v3/user/status') {
    // `allow_byok >= 2` is what `ta()` reads to decide that custom providers are
    // allowed. Below that the `mockprov` model is never registered and the model
    // call falls through to the real Qoder API.
    writeJSON(response, 200, { code: 0, success: true, data: {}, featureSwitches: { allow_byok: 2 } })
    return true
  }
  if (url.pathname === '/algo/api/v2/model/list' || url.pathname === '/api/v2/user/plan') {
    writeJSON(response, 200, { code: 0, success: true, data: {} })
    return true
  }
  if (url.pathname === '/ide-text/latest') {
    writeJSON(response, 200, { code: 0, success: true, data: {} })
    return true
  }
  return false
}

function modelCatalog(models: string[]): Record<string, unknown> {
  return {
    object: 'list',
    data: models.map(id => ({
      id,
      name: id,
      object: 'model',
      created: 1,
      owned_by: 'leapmux-e2e',
      capabilities: modelCapabilities(),
    })),
  }
}

function modelCapabilities(): Record<string, unknown> {
  return {
    supports: { vision: true },
    limits: { max_context_window_tokens: 128_000 },
  }
}

function protocolFor(method: string | undefined, path: string): MockModelProtocol | undefined {
  if (method !== 'POST')
    return undefined
  if (/\/(?:v1\/)?chat\/completions$/.test(path))
    return 'openai-chat-completions'
  if (/\/(?:v1\/)?responses$/.test(path))
    return 'openai-responses'
  if (/\/(?:v1\/)?messages$/.test(path))
    return 'anthropic-messages'
  return undefined
}

function isModelsPath(path: string): boolean {
  return /\/(?:v1\/)?models$/.test(path)
}

async function readJSONBody(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = []
  let size = 0
  for await (const chunk of request) {
    const bytes = Buffer.from(chunk)
    size += bytes.byteLength
    if (size > MAX_REQUEST_BYTES)
      throw new Error(`The request body exceeds ${MAX_REQUEST_BYTES} bytes`)
    chunks.push(bytes)
  }
  const text = Buffer.concat(chunks).toString('utf8')
  if (!text)
    throw new Error('The request body is empty')
  try {
    return JSON.parse(text)
  }
  catch (error) {
    throw new Error('The request body is not valid JSON', { cause: error })
  }
}

async function writeModelResponse(response: ServerResponse, protocol: MockModelProtocol, body: unknown, step: MockModelStep, id: string): Promise<void> {
  switch (protocol) {
    case 'openai-chat-completions':
      await writeOpenAIChatCompletion(response, body, step, id)
      return
    case 'openai-responses':
      await writeOpenAIResponse(response, body, step, id)
      return
    case 'anthropic-messages':
      await writeAnthropicMessage(response, body, step, id)
      return
    case 'aws-event-stream':
      // `protocolFor` never answers this protocol: Kiro's requests take their own
      // route above, before the path chooses a protocol.
      throw new Error('The Kiro surface writes an AWS event stream answer, and no model route writes one')
  }
}

async function writeOpenAIChatCompletion(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string): Promise<void> {
  const model = modelFrom(requestBody)
  const toolCalls = step.toolCalls?.map((tool, index) => ({
    index,
    id: tool.id,
    type: 'function',
    function: { name: tool.name, arguments: tool.input ?? JSON.stringify(tool.arguments) },
  }))
  const message = {
    role: 'assistant',
    ...(step.reasoning !== undefined ? { reasoning_content: step.reasoning } : {}),
    ...(step.text !== undefined ? { content: step.text } : { content: null }),
    ...(toolCalls ? { tool_calls: toolCalls.map(({ index: _index, ...tool }) => tool) } : {}),
  }
  if (!streamRequested(requestBody)) {
    writeJSON(response, 200, {
      id,
      object: 'chat.completion',
      created: 1,
      model,
      choices: [{ index: 0, message, finish_reason: toolCalls ? 'tool_calls' : 'stop' }],
      usage: usage('prompt_tokens', 'completion_tokens'),
    })
    return
  }

  writeSSEHeaders(response)
  const chunks = textChunks(step)
  // The FIRST chunk rides with the role, the reasoning and the tool calls, so a
  // step that streams no text writes exactly the one delta it always wrote.
  writeSSEData(response, {
    id,
    object: 'chat.completion.chunk',
    created: 1,
    model,
    choices: [{
      index: 0,
      // `reasoning_content` is the field the OpenAI-compatible providers of
      // GLM and DeepSeek use, which is what the agents here are configured for.
      delta: {
        role: 'assistant',
        ...(step.reasoning !== undefined ? { reasoning_content: step.reasoning } : {}),
        ...(chunks.length > 0 ? { content: chunks[0] } : {}),
        ...(toolCalls ? { tool_calls: toolCalls } : {}),
      },
      finish_reason: null,
    }],
  })
  for (const chunk of chunks.slice(1)) {
    await pauseBetweenChunks(response, step.stream?.delayMs ?? 0)
    if (response.writableEnded)
      return
    writeSSEData(response, {
      id,
      object: 'chat.completion.chunk',
      created: 1,
      model,
      choices: [{ index: 0, delta: { content: chunk }, finish_reason: null }],
    })
  }
  writeSSEData(response, {
    id,
    object: 'chat.completion.chunk',
    created: 1,
    model,
    choices: [{ index: 0, delta: {}, finish_reason: toolCalls ? 'tool_calls' : 'stop' }],
  })
  writeSSEData(response, {
    id,
    object: 'chat.completion.chunk',
    created: 1,
    model,
    choices: [],
    usage: usage('prompt_tokens', 'completion_tokens'),
  })
  response.end('data: [DONE]\n\n')
}

async function writeOpenAIResponse(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string): Promise<void> {
  const output = responseItems(step, id)
  if (!streamRequested(requestBody)) {
    writeJSON(response, 200, {
      id,
      object: 'response',
      status: 'completed',
      model: modelFrom(requestBody),
      output,
      usage: responseUsage(),
    })
    return
  }

  writeSSEHeaders(response)
  writeSSEEvent(response, { type: 'response.created', response: { id } })
  const chunks = textChunks(step)
  for (const [outputIndex, item] of output.entries()) {
    // The MESSAGE item is the one a client watches grow, so its text arrives as
    // deltas before the item that completes it. Every other item type is a
    // single decision and stays one `output_item.done`.
    if (item.type === 'message' && chunks.length > 0) {
      writeSSEEvent(response, { type: 'response.output_item.added', output_index: outputIndex, item: { ...item, status: 'in_progress', content: [] } })
      for (const [chunkIndex, chunk] of chunks.entries()) {
        if (chunkIndex > 0)
          await pauseBetweenChunks(response, step.stream?.delayMs ?? 0)
        if (response.writableEnded)
          return
        writeSSEEvent(response, {
          type: 'response.output_text.delta',
          output_index: outputIndex,
          item_id: item.id,
          content_index: 0,
          delta: chunk,
        })
      }
      writeSSEEvent(response, {
        type: 'response.output_text.done',
        output_index: outputIndex,
        item_id: item.id,
        content_index: 0,
        text: step.text ?? '',
      })
    }
    writeSSEEvent(response, { type: 'response.output_item.done', output_index: outputIndex, item })
  }
  writeSSEEvent(response, { type: 'response.completed', response: { id, status: 'completed', output, usage: responseUsage() } })
  response.end()
}

function responseItems(step: MockModelStep, responseID: string): Record<string, unknown>[] {
  const items: Record<string, unknown>[] = []
  if (step.reasoning !== undefined) {
    // `summary` is what a client shows; `content` is the full text. Codex's
    // `ResponseItem::Reasoning` reads both (codex-rs/protocol/src/models.rs).
    items.push({
      type: 'reasoning',
      id: `${responseID}-reasoning`,
      summary: [{ type: 'summary_text', text: step.reasoning }],
      content: [{ type: 'reasoning_text', text: step.reasoning }],
      encrypted_content: null,
    })
  }
  if (step.text !== undefined) {
    items.push({
      type: 'message',
      role: 'assistant',
      id: `${responseID}-message`,
      status: 'completed',
      content: [{ type: 'output_text', text: step.text, annotations: [] }],
    })
  }
  for (const tool of step.toolCalls ?? []) {
    // A CUSTOM tool takes raw text, so it travels as its own item type. Codex's
    // `exec` is one: its runtime evaluates the input as JavaScript source.
    items.push(tool.input === undefined
      ? {
          type: 'function_call',
          id: `${responseID}-${tool.id}`,
          call_id: tool.id,
          // A NAMESPACE rides beside the name rather than inside it. Codex reads
          // `item.namespace` and answers `unsupported call: <name>` in the tool
          // OUTPUT when a namespaced tool arrives without one.
          ...(tool.namespace === undefined ? {} : { namespace: tool.namespace }),
          name: tool.name,
          arguments: JSON.stringify(tool.arguments),
          status: 'completed',
        }
      : {
          type: 'custom_tool_call',
          id: `${responseID}-${tool.id}`,
          call_id: tool.id,
          name: tool.name,
          input: tool.input,
          status: 'completed',
        })
  }
  return items
}

async function writeAnthropicMessage(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string): Promise<void> {
  const content: Record<string, unknown>[] = []
  // A thinking block carries a signature the client echoes back on the next
  // turn. Its value is opaque to the client, so a fixed one is enough here.
  if (step.reasoning !== undefined)
    content.push({ type: 'thinking', thinking: step.reasoning, signature: 'leapmux-e2e-signature' })
  if (step.text !== undefined)
    content.push({ type: 'text', text: step.text })
  for (const tool of step.toolCalls ?? []) {
    // `tool_use.input` is a JSON object in this protocol, and it has no custom
    // tool. A step that states raw text cannot be expressed here, so say that
    // rather than send a shape the client will misread.
    if (tool.input !== undefined)
      throw new Error(`The Anthropic Messages protocol has no custom tool, so tool call ${tool.name} cannot state raw input`)
    if (tool.namespace !== undefined)
      throw new Error(`The Anthropic Messages protocol has no tool namespace, so tool call ${tool.name} cannot state one`)
    content.push({ type: 'tool_use', id: tool.id, name: tool.name, input: tool.arguments })
  }
  const stopReason = step.toolCalls?.length ? 'tool_use' : 'end_turn'
  if (!streamRequested(requestBody)) {
    writeJSON(response, 200, {
      id,
      type: 'message',
      role: 'assistant',
      model: modelFrom(requestBody),
      content,
      stop_reason: stopReason,
      stop_sequence: null,
      usage: { input_tokens: 1, output_tokens: 1 },
    })
    return
  }

  writeSSEHeaders(response)
  writeSSEEvent(response, {
    type: 'message_start',
    message: {
      id,
      type: 'message',
      role: 'assistant',
      model: modelFrom(requestBody),
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: { input_tokens: 1, output_tokens: 0 },
    },
  })
  let index = 0
  if (step.reasoning !== undefined) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'thinking', thinking: '' } })
    writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'thinking_delta', thinking: step.reasoning } })
    writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'signature_delta', signature: 'leapmux-e2e-signature' } })
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  const chunks = textChunks(step)
  if (chunks.length > 0) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'text', text: '' } })
    for (const [chunkIndex, chunk] of chunks.entries()) {
      if (chunkIndex > 0)
        await pauseBetweenChunks(response, step.stream?.delayMs ?? 0)
      if (response.writableEnded)
        return
      writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'text_delta', text: chunk } })
    }
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  for (const tool of step.toolCalls ?? []) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'tool_use', id: tool.id, name: tool.name, input: {} } })
    writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'input_json_delta', partial_json: JSON.stringify(tool.arguments) } })
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  writeSSEEvent(response, { type: 'message_delta', delta: { stop_reason: stopReason, stop_sequence: null }, usage: { output_tokens: 1 } })
  writeSSEEvent(response, { type: 'message_stop' })
  response.end()
}

function writeModelError(response: ServerResponse, protocol: MockModelProtocol, error: MockModelError): void {
  if (protocol === 'anthropic-messages') {
    writeJSON(response, error.status, { type: 'error', error: { type: error.code ?? 'api_error', message: error.message } })
    return
  }
  writeJSON(response, error.status, { error: { type: error.code ?? 'api_error', code: error.code, message: error.message } })
}

function modelFrom(body: unknown): string {
  return isRecord(body) && typeof body.model === 'string' ? body.model : 'mock-model'
}

/**
 * Whether a request asks for a stream of events rather than one JSON body.
 *
 * All three model APIs stream only when the body states `stream: true`, and
 * answer with one JSON body when the field is absent. A client that omits the
 * field reads the answer as one JSON document, and an event stream in its place
 * fails to parse. Two clients omit it:
 *
 * - MiMo Code's goal judge asks for a structured verdict through the AI SDK's
 *   non-streaming call.
 * - Codewhale sends its subagents' requests that way. A failed parse makes the
 *   child retry the same turn until the runtime gives up on it.
 */
function streamRequested(body: unknown): boolean {
  return isRecord(body) && body.stream === true
}

function usage(inputKey: string, outputKey: string): Record<string, number> {
  return { [inputKey]: 1, [outputKey]: 1, total_tokens: 2 }
}

function responseUsage(): Record<string, unknown> {
  return {
    input_tokens: 1,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens: 1,
    output_tokens_details: { reasoning_tokens: 0 },
    total_tokens: 2,
  }
}

function writeSSEHeaders(response: ServerResponse): void {
  response.writeHead(200, {
    'content-type': 'text/event-stream',
    'cache-control': 'no-cache',
    'connection': 'close',
  })
}

function writeSSEData(response: ServerResponse, value: unknown): void {
  response.write(`data: ${JSON.stringify(value)}\n\n`)
}

function writeSSEEvent(response: ServerResponse, value: { type: string } & Record<string, unknown>): void {
  response.write(`event: ${value.type}\ndata: ${JSON.stringify(value)}\n\n`)
}

function writeJSON(response: ServerResponse, status: number, value: unknown): void {
  response.writeHead(status, { 'content-type': 'application/json' }).end(JSON.stringify(value))
}
