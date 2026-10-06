import type { Buffer } from 'node:buffer'
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'
import type { Duplex } from 'node:stream'
import type { DisconnectSignals } from './mockHttp'
import type { MockModelScriptHost, MockSurface, ModelRequestContext } from './mockModelRequest'
import type {
  MockModelDeliveredError,
  MockModelError,
  MockModelProtocol,
  MockModelRequestRecord,
  MockModelRule,
  MockModelScenarioSpec,
  MockModelScenarioStatus,
  MockModelStep,
  MockModelUnexpectedRequest,
} from './mockModelScript'
import type { ModelStream } from './modelStream'
import { createServer } from 'node:http'
import { createServer as createHttp2Server } from 'node:http2'
import { isObject } from '../../../src/lib/jsonPick'
import { LOOPBACK_HOSTNAMES } from './agentEnvironmentInputs'
import { ampScriptOptions, createAmpSurface } from './ampSurface'
import { claudeLifecycleAnswer, prepareClaudeMessageStep } from './claudeSurface'
import { copilotCatalogMetadata, copilotReasoningFields, handleCopilotHttp } from './copilotSurface'
import { createCursorSurface } from './cursorSurface'
import { handleDroidHttp } from './droidSurface'
import { createDualVersionListener } from './dualVersionListener'
import { handleGoogleModelHttp } from './googleModelApi'
import { handleKiroHttp, kiroRequestMetadata } from './kiroSurface'
import { MOCK_IDENTITY_TOKEN, MOCK_MODELS, MOCK_SESSION_TOKEN, MODEL_KEY } from './mockAgentEnvironment'
import { mockCredentialReceipt } from './mockCredentials'
import { readJSONBody, waitUnlessDisconnected, writeMockJSON, writeResponseHeaders } from './mockHttp'
import {
  lastUserText,
  matchesRequest,
  MAX_SCENARIO_REQUEST_RECORDS,
  parseScenarioSpec,
  resolveStepCaptures,
  selectScenarioID,
  systemText,
  textChunks,
  validateGateName,
  validateScenarioID,
} from './mockModelScript'
import { rateLimitHeaders } from './mockRateLimitHeaders'
import { bufferModelOutput, createBufferedModelStream, createModelStream } from './modelStream'
import { handleQoderHttp } from './qoderSurface'

const MAX_HTTP_REQUEST_RECORDS = 10_000
const MAX_UNMATCHED_RECORDS = 200

/**
 * Limit the number of responses that one scenario's fallback supplies.
 *
 * A test can need a fallback because it cannot predict the turn count.
 * Some providers start extra turns. Codex continues while a session goal is active.
 * An unlimited fallback can produce an endless loop and stop Playwright before the test reports a failure.
 *
 * At the limit, record the request as unexpected and fail its turn.
 * The request record identifies the loop.
 */
const MAX_FALLBACK_ANSWERS = 200

export interface MockModelHTTPRequestRecord {
  method: string
  path: string
  /**
   * The operation of a Kiro service request.
   * Kiro's model calls supply the operation in a header.
   * Kiro's remote catalog calls supply the operation in the service path.
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
   * The proxy rejection count for each host.
   * Count CONNECT tunnels by host:port.
   * Count absolute-form requests to another host also.
   * The suite reports these counts at shutdown, so rejected host attempts stay visible.
   */
  refusedHosts: () => ReadonlyMap<string, number>
}

/** Whether a request target is in absolute form (`http://host/path`), as a proxy receives it. */
function isAbsoluteForm(target: string): boolean {
  return /^[a-z][\w+.-]*:\/\//i.test(target)
}

/** Whether an absolute-form target addresses the mock itself. */
function isOwnOrigin(target: URL, ownPort: number): boolean {
  return LOOPBACK_HOSTNAMES.has(target.hostname) && Number(target.port) === ownPort
}

interface ScenarioState {
  spec: MockModelScenarioSpec
  nextStep: number
  /** The fallback request count. See MAX_FALLBACK_ANSWERS. */
  fallbackAnswers: number
  ruleMatches: Map<string, number>
  gates: Map<string, ModelGate>
  closed: boolean
  requests: MockModelRequestRecord[]
  unexpectedRequests: MockModelUnexpectedRequest[]
}

interface ModelGate {
  released: boolean
  waiters: Set<(released: boolean) => void>
}

/** Capture headers after the native response completes without retaining any request credential. */
function recordModelResponse(response: ServerResponse, record: MockModelRequestRecord, readError: () => MockModelDeliveredError | undefined): void {
  response.once('finish', () => {
    const headers = Object.fromEntries(Object.entries(response.getHeaders()).map(([key, value]) => [key, Array.isArray(value) ? value.join(', ') : String(value ?? '')]))
    const error = readError()
    record.response = {
      status: response.statusCode,
      headers,
      ...(error ? { serviceError: { code: error.code, message: error.message } } : {}),
    }
  })
}

/** The step that answers one model request, or the reason that no step answers it. */
type ScenarioAnswer
  = | { kind: 'step', step: MockModelStep, scenario: ScenarioState, record: MockModelRequestRecord }
    | { kind: 'missing', message: string }

/**
 * Start a strict server for the coding agents' model requests.
 *
 * Model APIs:
 * - Anthropic Messages.
 * - OpenAI Chat Completions.
 * - OpenAI Responses.
 * - Google Generative Language.
 *
 * The `surfaces` list below holds the native agent services and the Google API. Each
 * entry implements `MockSurface`.
 *
 * Scripts supply model answers. Startup calls use isolated model and account metadata.
 * A changed provider prompt can fail a matching rule. The scripted answer stays fixed.
 * See ./mockModelScript for the vocabulary and ./mockModelScenario for the registration client.
 */
export async function createMockModelServer(options: MockModelServerOptions): Promise<MockModelServer> {
  if (options.models.length === 0)
    throw new Error('The mock model server needs at least one model identifier.')
  const models = [...options.models]
  const scenarios = new Map<string, ScenarioState>()
  const http: MockModelHTTPRequestRecord[] = []
  const unmatched: MockModelUnmatchedRequest[] = []
  const refusedHosts = new Map<string, number>()
  let responseSequence = 0
  // Set the listening port before the server handles a request.
  let ownPort = 0

  // Record one request that the proxy refused, and count its host.
  const refuse = (host: string, record: MockModelHTTPRequestRecord): void => {
    http.push(record)
    if (http.length > MAX_HTTP_REQUEST_RECORDS)
      http.shift()
    refusedHosts.set(host, (refusedHosts.get(host) ?? 0) + 1)
  }

  // Record unregistered scenario requests consistently across all supported model routes.
  const answerFor = (context: ModelRequestContext, capabilities?: { allowServiceToolMetadata?: boolean }): ScenarioAnswer => {
    const scenarioID = context.scenarioID ?? selectScenarioID(context.protocol, context.body)
    const scenario = scenarios.get(scenarioID)
    if (!scenario) {
      recordUnmatched(unmatched, { ...context, scenarioID, reason: 'The scenario is not registered.' })
      return { kind: 'missing', message: `The model scenario ${scenarioID} is not registered.` }
    }
    const step = selectStep(scenario, context)
    if (!step)
      return { kind: 'missing', message: `The model scenario ${scenarioID} has no answer for this request. Its status lists the reason.` }
    const record = scenario.requests.at(-1)
    if (!record)
      throw new Error('A selected model step has no native request record.')
    if (!capabilities?.allowServiceToolMetadata && step.toolCalls?.some(tool => tool.completionGate !== undefined || tool.taskProgress !== undefined || tool.nativeExecution !== undefined)) {
      const reason = 'This native route does not support metadata for provider service tools.'
      scenario.unexpectedRequests.push({ protocol: context.protocol, path: context.path, body: context.body, reason })
      throw new Error(reason)
    }
    return { kind: 'step', step, scenario, record }
  }

  const scriptHost: MockModelScriptHost = {
    hasScenario: id => scenarios.has(id),
    select: (context, capabilities) => {
      const answer = answerFor(context, capabilities)
      if (answer.kind === 'missing')
        return answer
      const { scenario, record, step } = answer
      return {
        kind: 'step',
        step,
        isClosed: () => scenario.closed,
        holdGate: (name, transport) => holdGate(scenario, name, transport),
        holdStep: transport => holdStep(scenario, step, transport),
        stream: (response, request) => createModelStream(response, step.stream, name => holdGate(scenario, name, { request, response })),
        bufferGeneration: (signal) => {
          const stream = createBufferedModelStream(signal, step.stream, name => holdGate(scenario, name, { signal }))
          return bufferModelOutput(stream, step)
        },
        recordHttpResponse: (response, readError) => recordModelResponse(response, record, readError),
        recordServiceError: error => (record.serviceResponse = error),
      }
    },
  }
  const copilotOptions = { sessionToken: MOCK_SESSION_TOKEN, defaultModelId: MOCK_MODELS.openai, reasoningModelId: MOCK_MODELS.gooseReasoning }
  const qoderOptions = { origin: () => `http://127.0.0.1:${ownPort}`, identityToken: MOCK_IDENTITY_TOKEN }
  // The native services, in the order that the server offers them a request. A request
  // that no surface claims reaches the model routes below.
  const surfaces: readonly MockSurface[] = [
    { handleHttp: (request, response, url) => handleDroidHttp(request, response, url, { modelKey: MODEL_KEY }) },
    { handleHttp: (request, response, url) => handleCopilotHttp(request, response, url, copilotOptions) },
    { handleHttp: (request, response, url) => handleQoderHttp(request, response, url, qoderOptions) },
    createAmpSurface(ampScriptOptions(scriptHost)),
    createCursorSurface(scriptHost),
    { handleHttp: (request, response, url) => handleKiroHttp(request, response, url, scriptHost) },
    { handleHttp: (request, response, url) => handleGoogleModelHttp(request, response, url, scriptHost) },
  ]
  const clearScenario = (scenarioID: string): void => {
    for (const surface of surfaces)
      surface.clearScenario?.(scenarioID)
  }

  const handle = async (request: IncomingMessage, response: ServerResponse): Promise<void> => {
    try {
      // Routing another host by path can treat its request as a local model request.
      // Reject absolute-form targets for other hosts.
      // Serve targets for this mock as direct requests.
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
        await handleScenarioControl(request, response, url, scenarios, clearScenario)
        return
      }
      if (request.method === 'GET' && isModelsPath(url.pathname)) {
        writeJSON(response, 200, modelCatalog(models))
        return
      }
      for (const surface of surfaces) {
        if (await surface.handleHttp(request, response, url))
          return
      }
      const protocol = protocolFor(request.method, url.pathname)
      if (!protocol) {
        writeJSON(response, 404, { error: { message: `The mock server has no route for ${request.method ?? 'UNKNOWN'} ${url.pathname}.` } })
        return
      }
      const body = await readJSONBody(request)
      const context: ModelRequestContext = {
        protocol,
        path: url.pathname,
        body,
        systemText: systemText(protocol, body),
        userText: lastUserText(protocol, body),
        mockCredential: mockCredentialReceipt(request.headers),
        ...(request.headers['anthropic-beta'] === undefined
          ? {}
          : {
              requestHeaders: { 'anthropic-beta': Array.isArray(request.headers['anthropic-beta']) ? request.headers['anthropic-beta'].join(', ') : request.headers['anthropic-beta'] },
            }),
      }
      const answer = answerFor(context)
      if (answer.kind === 'missing') {
        writeJSON(response, 409, { error: { message: answer.message } })
        return
      }
      const { step, scenario } = answer
      recordModelResponse(response, answer.record, () => step.error ? genericModelError(step.error) : undefined)
      if (!await holdStep(scenario, step, { request, response }))
        return
      if (step.error) {
        writeModelError(response, protocol, body, step.error)
        return
      }
      responseSequence++
      await writeModelResponse(response, protocol, body, step, `mock-response-${responseSequence}`, createModelStream(response, step.stream, name => holdGate(scenario, name, { request, response })))
    }
    catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      if (!response.headersSent)
        writeJSON(response, 400, { error: { message } })
      else
        response.destroy(error instanceof Error ? error : new Error(message))
    }
  }

  // Separate HTTP/1.1 and HTTP/2 servers share one listening port.
  // Cursor uses HTTP/2 for Run and HTTP/1.1 for startup calls.
  // The listener selects a server from the socket preface.
  // See ./dualVersionListener for the Node protocol limitation.
  const http1 = createServer(handle)
  // The suite configures this server as the HTTPS proxy for leapmux dev and its child processes.
  // Reject every CONNECT tunnel and every absolute-form target for another host.
  // Proxy-aware telemetry and update clients then fail without contacting their upstream hosts.
  http1.on('connect', (request: IncomingMessage, socket: Duplex) => {
    const host = request.url ?? ''
    refuse(host, { method: 'CONNECT', path: host, status: 403 })
    socket.on('error', () => socket.destroy())
    socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
  })
  const http2 = createHttp2Server(handle as never)
  const listener = createDualVersionListener(http1, http2)
  const { server } = listener
  http1.on('upgrade', (request: IncomingMessage, socket: Duplex, head: Buffer) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (!surfaces.some(surface => surface.handleUpgrade?.(request, socket, head, url)))
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
    close: () => {
      for (const scenario of scenarios.values())
        cancelGates(scenario)
      for (const surface of surfaces)
        surface.close?.()
      return listener.close()
    },
  }
}

/**
 * Resolve each selected step's captures from its request.
 *
 * If a capture does not match, record an unexpected request and return no step.
 * An exhausted queue returns no step also.
 */
function selectStep(scenario: ScenarioState, context: ModelRequestContext): MockModelStep | undefined {
  const step = chooseStep(scenario, context)
  if (!step)
    return undefined
  let resolved: ReturnType<typeof resolveStepCaptures>
  try {
    resolved = resolveStepCaptures(step, context.body)
  }
  catch (error) {
    scenario.unexpectedRequests.push({
      protocol: context.protocol,
      path: context.path,
      reason: error instanceof Error ? error.message : String(error),
      body: context.body,
    })
    return undefined
  }
  if ('step' in resolved)
    return resolved.step
  scenario.unexpectedRequests.push({
    protocol: context.protocol,
    path: context.path,
    reason: `Capture ${resolved.unmatchedCapture} matched nothing in the request.`,
    body: context.body,
  })
  return undefined
}

/**
 * Choose a scripted answer for one request.
 *
 * Matching rules take priority over the ordered queue.
 * This keeps matching housekeeping requests from consuming test turns.
 * If the queue ends with no available fallback, record an unexpected request.
 * That unexpected request keeps the scenario incomplete.
 */
function chooseStep(scenario: ScenarioState, context: ModelRequestContext): MockModelStep | undefined {
  const deliveredChild = claudeLifecycleAnswer(context)
  if (deliveredChild) {
    const name = deliveredChild.rule
    scenario.ruleMatches.set(name, (scenario.ruleMatches.get(name) ?? 0) + 1)
    recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, rule: name, body: context.body }, context)
    return deliveredChild.step
  }
  const rule = findRule(scenario, context)
  if (rule) {
    scenario.ruleMatches.set(rule.name, (scenario.ruleMatches.get(rule.name) ?? 0) + 1)
    recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, rule: rule.name, body: context.body }, context)
    return rule.respond
  }
  if (scenario.nextStep >= scenario.spec.steps.length) {
    const { fallback } = scenario.spec
    if (fallback && scenario.fallbackAnswers < MAX_FALLBACK_ANSWERS) {
      scenario.fallbackAnswers++
      recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, fallback: true, body: context.body }, context)
      return fallback
    }
    if (fallback) {
      scenario.unexpectedRequests.push({
        protocol: context.protocol,
        path: context.path,
        reason: `The fallback reached its limit of ${MAX_FALLBACK_ANSWERS} answers. The provider did not finish its repeated turns.`,
        body: context.body,
      })
      return undefined
    }
    scenario.unexpectedRequests.push({
      protocol: context.protocol,
      path: context.path,
      reason: 'The scenario has no remaining scripted answer.',
      body: context.body,
    })
    return undefined
  }
  const stepIndex = scenario.nextStep++
  recordScenarioRequest(scenario, { protocol: context.protocol, path: context.path, stepIndex, body: context.body }, context)
  return scenario.spec.steps[stepIndex]
}

/** Record a selected model request. Remove the oldest records above the request limit. */
function recordScenarioRequest(scenario: ScenarioState, record: MockModelRequestRecord, context: Pick<ModelRequestContext, 'serverContext' | 'mockCredential' | 'requestHeaders' | 'nativeRequest'>): void {
  scenario.requests.push({
    ...record,
    ...(context.serverContext ? { serverContext: context.serverContext } : {}),
    ...(context.mockCredential ? { mockCredential: context.mockCredential } : {}),
    ...(context.requestHeaders ? { requestHeaders: context.requestHeaders } : {}),
    ...(context.nativeRequest === undefined ? {} : { nativeRequest: context.nativeRequest }),
  })
  if (scenario.requests.length > MAX_SCENARIO_REQUEST_RECORDS)
    scenario.requests.splice(0, scenario.requests.length - MAX_SCENARIO_REQUEST_RECORDS)
}

function findRule(scenario: ScenarioState, context: ModelRequestContext): MockModelRule | undefined {
  const matches = (rule: MockModelRule): boolean => {
    if (rule.once && (scenario.ruleMatches.get(rule.name) ?? 0) > 0)
      return false
    return matchesRequest(rule.when, context)
  }
  return scenario.spec.rules.find(rule => rule.priority === 'high' && matches(rule))
    ?? scenario.spec.rules.find(rule => rule.priority !== 'high' && matches(rule))
}

/** Hold a step at its own gate or for its delay, by the rules that `SelectedModelAnswer.holdStep` states. */
function holdStep(scenario: ScenarioState, step: MockModelStep, transport: DisconnectSignals): Promise<boolean> {
  if (step.gate !== undefined)
    return holdGate(scenario, step.gate, transport)
  return waitUnlessDisconnected(step.delayMs ?? 0, transport)
}

/** Keep a model request open until its test releases the gate. */
function holdGate(scenario: ScenarioState, name: string, transport: DisconnectSignals): Promise<boolean> {
  const { request, response, signal } = transport
  if (scenario.closed)
    return Promise.resolve(false)
  let gate = scenario.gates.get(name)
  if (!gate) {
    gate = { released: false, waiters: new Set() }
    scenario.gates.set(name, gate)
  }
  if (gate.released)
    return Promise.resolve(true)
  const heldGate = gate
  return new Promise((resolve) => {
    let settled = false
    const onDisconnect = () => finish(false)
    function finish(released: boolean) {
      if (settled)
        return
      settled = true
      heldGate.waiters.delete(finish)
      request?.off('aborted', onDisconnect)
      response?.off('close', onDisconnect)
      signal?.removeEventListener('abort', onDisconnect)
      if (!released && response && !response.destroyed)
        response.destroy()
      resolve(released)
    }
    heldGate.waiters.add(finish)
    request?.once('aborted', onDisconnect)
    response?.once('close', onDisconnect)
    signal?.addEventListener('abort', onDisconnect, { once: true })
    if (request?.aborted || response?.destroyed || signal?.aborted)
      finish(false)
  })
}

/** Release every request that waits on one gate. */
function releaseGate(scenario: ScenarioState, name: string): boolean {
  const gate = scenario.gates.get(name)
  if (scenario.closed || !gate || gate.released || gate.waiters.size === 0)
    return false
  gate.released = true
  for (const finish of [...gate.waiters])
    finish(true)
  return true
}

/** Cancel held requests before a scenario or the mock server closes. */
function cancelGates(scenario: ScenarioState): void {
  scenario.closed = true
  for (const gate of scenario.gates.values()) {
    for (const finish of [...gate.waiters])
      finish(false)
  }
  scenario.gates.clear()
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
  writeJSON(response, 405, { error: { message: `The HTTP method ${request.method ?? 'UNKNOWN'} is not allowed.` } })
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
    ...kiroRequestMetadata(request),
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
  onDelete: (id: string) => void,
): Promise<void> {
  const suffix = url.pathname.slice('/__e2e/scenarios/'.length)
  const release = /^([^/]+)\/gates\/([^/]+)\/(release|release-if-held)$/.exec(suffix)
  if (release) {
    const id = decodeURIComponent(release[1]!)
    const gateName = decodeURIComponent(release[2]!)
    validateScenarioID(id)
    validateGateName(gateName)
    const scenario = scenarios.get(id)
    if (!scenario) {
      writeJSON(response, 404, { error: { message: `The model scenario ${id} does not exist.` } })
      return
    }
    if (request.method !== 'POST') {
      writeJSON(response, 405, { error: { message: `The HTTP method ${request.method ?? 'UNKNOWN'} is not allowed.` } })
      return
    }
    const released = releaseGate(scenario, gateName)
    if (release[3] === 'release-if-held') {
      writeJSON(response, 200, { released })
      return
    }
    if (!released) {
      writeJSON(response, 409, { error: { message: `The model gate ${gateName} has no waiting request.` } })
      return
    }
    response.writeHead(204).end()
    return
  }
  const id = decodeURIComponent(suffix)
  validateScenarioID(id)
  if (request.method === 'PUT') {
    if (scenarios.has(id)) {
      writeJSON(response, 409, { error: { message: `The model scenario ${id} already exists.` } })
      return
    }
    const spec = parseScenarioSpec(await readJSONBody(request))
    const state: ScenarioState = { spec, nextStep: 0, fallbackAnswers: 0, ruleMatches: new Map(), gates: new Map(), closed: false, requests: [], unexpectedRequests: [] }
    scenarios.set(id, state)
    writeJSON(response, 201, scenarioStatus(state))
    return
  }

  const scenario = scenarios.get(id)
  if (!scenario) {
    writeJSON(response, 404, { error: { message: `The model scenario ${id} does not exist.` } })
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
    const force = url.searchParams.get('force') === 'true'
    const allowUnconsumed = url.searchParams.get('allow-unconsumed') === 'true'
    if (force && allowUnconsumed) {
      writeJSON(response, 400, { error: { message: 'Scenario deletion cannot combine force and allow-unconsumed.' } })
      return
    }
    const status = scenarioStatus(scenario)
    // Verify and remove in one synchronous branch so a later request cannot escape the policy check.
    const verified = allowUnconsumed ? status.unexpectedRequests.length === 0 : status.complete
    if (!force && !verified) {
      writeJSON(response, 409, status)
      return
    }
    cancelGates(scenario)
    scenarios.delete(id)
    onDelete(id)
    response.writeHead(204).end()
    return
  }
  writeJSON(response, 405, { error: { message: `The HTTP method ${request.method ?? 'UNKNOWN'} is not allowed.` } })
}

/**
 * Append new steps to the ordered queue.
 *
 * Insert new rules before existing rules.
 * Higher priority rules still match first.
 * Within one priority, a new matching rule takes precedence over an earlier rule.
 * Registration supplies the housekeeping rules.
 */
function extendScenario(scenario: ScenarioState, addition: MockModelScenarioSpec): void {
  const names = new Set(scenario.spec.rules.map(rule => rule.name))
  for (const rule of addition.rules) {
    if (names.has(rule.name))
      throw new Error(`The script declares model rule ${rule.name} twice.`)
    names.add(rule.name)
  }
  scenario.spec = {
    steps: [...scenario.spec.steps, ...addition.steps],
    rules: [...addition.rules, ...scenario.spec.rules],
    // Replace the current fallback only when the addition supplies one.
    // Reuse that response after the ordered queue, up to the fallback limit.
    ...(addition.fallback ?? scenario.spec.fallback ? { fallback: addition.fallback ?? scenario.spec.fallback } : {}),
  }
}

function scenarioStatus(scenario: ScenarioState): MockModelScenarioStatus {
  const pendingGates = [...scenario.gates]
    .filter(([, gate]) => gate.waiters.size > 0)
    .map(([name]) => name)
    .sort()
  return {
    complete: scenario.nextStep === scenario.spec.steps.length && scenario.unexpectedRequests.length === 0 && pendingGates.length === 0,
    nextStep: scenario.nextStep,
    stepCount: scenario.spec.steps.length,
    ruleMatches: Object.fromEntries(scenario.ruleMatches),
    pendingGates,
    requests: scenario.requests,
    unexpectedRequests: scenario.unexpectedRequests,
  }
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
      ...copilotCatalogMetadata(id, { defaultModelId: MOCK_MODELS.openai, reasoningModelId: MOCK_MODELS.gooseReasoning }),
    })),
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

async function writeModelResponse(response: ServerResponse, protocol: MockModelProtocol, body: unknown, step: MockModelStep, id: string, stream: ModelStream): Promise<void> {
  switch (protocol) {
    case 'openai-chat-completions':
      await writeOpenAIChatCompletion(response, body, step, id, stream)
      return
    case 'openai-responses':
      await writeOpenAIResponse(response, body, step, id, stream)
      return
    case 'anthropic-messages':
      await writeAnthropicMessage(response, body, prepareClaudeMessageStep(protocol, body, step, id), id, stream)
      return
    case 'google-generative-language':
      throw new Error('The Google model API handler owns this response.')
    case 'aws-event-stream':
      // The Kiro service route handles AWS event streams before protocol selection.
      // protocolFor returns only model API protocols.
      throw new Error('This model route cannot send the Kiro AWS event stream.')
  }
}

async function writeOpenAIChatCompletion(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string, stream: ModelStream): Promise<void> {
  const model = modelFrom(requestBody)
  const toolCalls = step.toolCalls?.length
    ? step.toolCalls.map((tool, index) => ({
        index,
        id: tool.id,
        ...(tool.input !== undefined
          ? { type: 'custom', custom: { name: tool.name, input: tool.input } }
          : { type: 'function', function: { name: tool.name, arguments: JSON.stringify(tool.arguments) } }),
      }))
    : undefined
  const message = {
    role: 'assistant',
    ...(step.reasoning !== undefined ? { reasoning_content: step.reasoning } : {}),
    ...(step.reasoning !== undefined ? copilotReasoningFields(model, step.reasoning, MOCK_MODELS.openai) : {}),
    ...(step.text !== undefined ? { content: step.text } : { content: null }),
    ...(toolCalls ? { tool_calls: toolCalls.map(({ index: _index, ...tool }) => tool) } : {}),
  }
  if (!streamRequested(requestBody)) {
    if (step.stream && !await bufferModelOutput(stream, step))
      return
    writeJSON(response, 200, {
      id,
      object: 'chat.completion',
      created: 1,
      model,
      choices: [{ index: 0, message, finish_reason: toolCalls ? 'tool_calls' : 'stop' }],
      usage: usage('prompt_tokens', 'completion_tokens', step),
    }, step)
    return
  }

  writeSSEHeaders(response, step)
  let emitted = false
  const emit = (delta: Record<string, unknown>) => {
    writeSSEData(response, {
      id,
      object: 'chat.completion.chunk',
      created: 1,
      model,
      choices: [{ index: 0, delta: { ...(!emitted ? { role: 'assistant' } : {}), ...delta }, finish_reason: null }],
    })
    emitted = true
  }
  for await (const chunk of stream.chunks(step.reasoning))
    emit({ reasoning_content: chunk, ...copilotReasoningFields(model, chunk, MOCK_MODELS.openai) })
  for await (const chunk of stream.chunks(step.text))
    emit({ content: chunk })
  if (!stream.active)
    return
  // The tool calls follow the text, as in the Responses and Anthropic writers. A
  // model writes a call after the text before it, so a native client that gets
  // the complete call first can run it while the text still streams.
  if (toolCalls)
    emit({ tool_calls: toolCalls })
  else if (!emitted)
    emit({})
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
    usage: usage('prompt_tokens', 'completion_tokens', step),
  })
  response.end('data: [DONE]\n\n')
}

async function writeOpenAIResponse(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string, stream: ModelStream): Promise<void> {
  const output = responseItems(step, id)
  const model = modelFrom(requestBody)
  const responseBase = { id, object: 'response', created_at: 1, model, tools: [] }
  const completed = { ...responseBase, status: 'completed', output, usage: responseUsage(step) }
  if (!streamRequested(requestBody)) {
    if (step.stream && !await bufferModelOutput(stream, step))
      return
    writeJSON(response, 200, completed, step)
    return
  }

  writeSSEHeaders(response, step)
  let sequenceNumber = 0
  const emit = (event: { type: string } & Record<string, unknown>) => {
    writeSSEEvent(response, { ...event, sequence_number: sequenceNumber++ })
  }
  emit({ type: 'response.created', response: { ...responseBase, status: 'in_progress', output: [] } })
  for (const [outputIndex, item] of output.entries()) {
    const startItem = item.type === 'message'
      ? { ...item, status: 'in_progress', content: [] }
      : item.type === 'reasoning'
        ? { ...item, status: 'in_progress', summary: [], content: [] }
        : item.type === 'function_call'
          ? { ...item, status: 'in_progress', arguments: '' }
          : { ...item, status: 'in_progress' }
    emit({ type: 'response.output_item.added', output_index: outputIndex, item: startItem })

    if (item.type === 'reasoning') {
      emit({ type: 'response.reasoning_summary_part.added', output_index: outputIndex, item_id: item.id, summary_index: 0, part: { type: 'summary_text', text: '' } })
      for await (const chunk of stream.chunks(step.reasoning))
        emit({ type: 'response.reasoning_summary_text.delta', output_index: outputIndex, item_id: item.id, summary_index: 0, delta: chunk })
      if (!stream.active)
        return
      emit({ type: 'response.reasoning_summary_text.done', output_index: outputIndex, item_id: item.id, summary_index: 0, text: step.reasoning })
      emit({ type: 'response.reasoning_summary_part.done', output_index: outputIndex, item_id: item.id, summary_index: 0, part: { type: 'summary_text', text: step.reasoning } })
    }
    if (item.type === 'message') {
      emit({ type: 'response.content_part.added', output_index: outputIndex, item_id: item.id, content_index: 0, part: { type: 'output_text', text: '', annotations: [] } })
      for await (const chunk of stream.chunks(step.text)) {
        emit({
          type: 'response.output_text.delta',
          output_index: outputIndex,
          item_id: item.id,
          content_index: 0,
          delta: chunk,
        })
      }
      if (!stream.active)
        return
      emit({
        type: 'response.output_text.done',
        output_index: outputIndex,
        item_id: item.id,
        content_index: 0,
        text: step.text ?? '',
      })
      emit({ type: 'response.content_part.done', output_index: outputIndex, item_id: item.id, content_index: 0, part: { type: 'output_text', text: step.text ?? '', annotations: [] } })
    }
    if (item.type === 'function_call') {
      emit({ type: 'response.function_call_arguments.delta', output_index: outputIndex, item_id: item.id, call_id: item.call_id, delta: item.arguments })
      emit({ type: 'response.function_call_arguments.done', output_index: outputIndex, item_id: item.id, call_id: item.call_id, arguments: item.arguments })
    }
    emit({ type: 'response.output_item.done', output_index: outputIndex, item })
  }
  emit({ type: 'response.completed', response: completed })
  response.end()
}

function responseItems(step: MockModelStep, responseID: string): Record<string, unknown>[] {
  const items: Record<string, unknown>[] = []
  if (step.reasoning !== undefined) {
    // The client displays summary. content contains the full reasoning text.
    // Codex reads both fields in codex-rs/protocol/src/models.rs.
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
    // A custom tool receives raw text in a separate response item.
    // Codex exec evaluates that input as JavaScript source.
    items.push(tool.input === undefined
      ? {
          type: 'function_call',
          id: `${responseID}-${tool.id}`,
          call_id: tool.id,
          // Write namespace beside the tool name. Codex reads item.namespace.
          // A missing namespace makes a namespaced call fail with "unsupported call: <name>".
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

async function writeAnthropicMessage(response: ServerResponse, requestBody: unknown, step: MockModelStep, id: string, stream: ModelStream): Promise<void> {
  const content: Record<string, unknown>[] = []
  // The client returns a thinking block's signature in its next request.
  // The signature is opaque, so a fixed mock value is sufficient.
  if (step.reasoning !== undefined)
    content.push({ type: 'thinking', thinking: step.reasoning, signature: 'leapmux-e2e-signature' })
  if (step.text !== undefined)
    content.push({ type: 'text', text: step.text })
  for (const tool of step.toolCalls ?? []) {
    // Anthropic tool_use.input requires a JSON object.
    // Reject raw string input before writing a response that the client cannot parse.
    if (tool.input !== undefined)
      throw new Error(`Anthropic Messages cannot send raw input for tool ${tool.name}.`)
    if (tool.namespace !== undefined)
      throw new Error(`Anthropic Messages cannot send a tool namespace for ${tool.name}.`)
    content.push({ type: 'tool_use', id: tool.id, name: tool.name, input: tool.arguments })
  }
  const stopReason = step.toolCalls?.length ? 'tool_use' : 'end_turn'
  const inputTokens = step.usage?.inputTokens ?? 1
  const outputTokens = step.usage?.outputTokens ?? 1
  if (!streamRequested(requestBody)) {
    if (step.stream && !await bufferModelOutput(stream, step))
      return
    writeJSON(response, 200, {
      id,
      type: 'message',
      role: 'assistant',
      model: modelFrom(requestBody),
      content,
      stop_reason: stopReason,
      stop_sequence: null,
      usage: { input_tokens: inputTokens, output_tokens: outputTokens },
    }, step)
    return
  }

  writeSSEHeaders(response, step)
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
      usage: { input_tokens: inputTokens, output_tokens: 0 },
    },
  })
  let index = 0
  if (step.reasoning !== undefined) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'thinking', thinking: '' } })
    for await (const chunk of stream.chunks(step.reasoning))
      writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'thinking_delta', thinking: chunk } })
    if (!stream.active)
      return
    writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'signature_delta', signature: 'leapmux-e2e-signature' } })
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  const chunks = textChunks(step)
  if (chunks.length > 0) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'text', text: '' } })
    for await (const chunk of stream.chunks(step.text)) {
      writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'text_delta', text: chunk } })
    }
    if (!stream.active)
      return
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  for (const tool of step.toolCalls ?? []) {
    writeSSEEvent(response, { type: 'content_block_start', index, content_block: { type: 'tool_use', id: tool.id, name: tool.name, input: {} } })
    writeSSEEvent(response, { type: 'content_block_delta', index, delta: { type: 'input_json_delta', partial_json: JSON.stringify(tool.arguments) } })
    writeSSEEvent(response, { type: 'content_block_stop', index })
    index++
  }
  writeSSEEvent(response, { type: 'message_delta', delta: { stop_reason: stopReason, stop_sequence: null }, usage: { output_tokens: outputTokens } })
  writeSSEEvent(response, { type: 'message_stop' })
  response.end()
}

function genericModelError(error: MockModelError): MockModelDeliveredError {
  return { code: error.code ?? 'api_error', message: error.message }
}

function writeModelError(response: ServerResponse, protocol: MockModelProtocol, requestBody: unknown, error: MockModelError): void {
  const delivered = genericModelError(error)
  if (error.midStream) {
    writeMidStreamModelError(response, protocol, requestBody, delivered)
    return
  }
  if (protocol === 'anthropic-messages') {
    writeJSON(response, error.status, { type: 'error', error: { type: delivered.code, message: delivered.message } })
    return
  }
  writeJSON(response, error.status, { error: { type: delivered.code, code: error.code, message: delivered.message } })
}

/**
 * Fail a Chat Completions stream after it started: one partial text delta, then
 * the error payload, then the end of the stream. The delta is what makes the
 * failure a mid-stream one. qodercli 1.1.65 replaces an error that comes before
 * any delta with its own generic text, as it does for an HTTP status error.
 */
function writeMidStreamModelError(response: ServerResponse, protocol: MockModelProtocol, requestBody: unknown, delivered: MockModelDeliveredError): void {
  if (protocol !== 'openai-chat-completions')
    throw new Error(`The mock sends a mid-stream model error on the OpenAI Chat Completions route only, not on ${protocol}.`)
  if (!streamRequested(requestBody))
    throw new Error('The mock sends a mid-stream model error only to a request that asks for a stream.')
  writeSSEHeaders(response)
  writeSSEData(response, {
    id: 'mock-mid-stream-error',
    object: 'chat.completion.chunk',
    created: 1,
    model: modelFrom(requestBody),
    choices: [{ index: 0, delta: { role: 'assistant', content: 'partial ' }, finish_reason: null }],
  })
  writeSSEData(response, { error: { message: delivered.message, code: delivered.code } })
  response.end()
}

function modelFrom(body: unknown): string {
  return isObject(body) && typeof body.model === 'string' ? body.model : 'mock-model'
}

/**
 * Return a JSON response unless request.stream is true.
 *
 * A client that omits stream expects one complete JSON document.
 * An event stream cannot replace that document.
 * MiMo Code uses this path for structured goal verdicts through the AI SDK.
 * Codewhale uses it for child requests.
 * A parse failure makes the child retry until its runtime stops.
 */
function streamRequested(body: unknown): boolean {
  return isObject(body) && body.stream === true
}

function usage(inputKey: string, outputKey: string, step?: MockModelStep): Record<string, number> {
  const input = step?.usage?.inputTokens ?? 1
  const output = step?.usage?.outputTokens ?? 1
  return { [inputKey]: input, [outputKey]: output, total_tokens: input + output }
}

function responseUsage(step?: MockModelStep): Record<string, unknown> {
  const input = step?.usage?.inputTokens ?? 1
  const output = step?.usage?.outputTokens ?? 1
  return {
    input_tokens: input,
    input_tokens_details: { cached_tokens: 0 },
    output_tokens: output,
    output_tokens_details: { reasoning_tokens: 0 },
    total_tokens: input + output,
  }
}

function writeSSEHeaders(response: ServerResponse, step?: MockModelStep): void {
  writeResponseHeaders(response, 200, {
    'content-type': 'text/event-stream',
    'cache-control': 'no-cache',
    'connection': 'close',
    ...rateLimitHeaders(step),
  })
}

function writeSSEData(response: ServerResponse, value: unknown): void {
  response.write(`data: ${JSON.stringify(value)}\n\n`)
}

function writeSSEEvent(response: ServerResponse, value: { type: string } & Record<string, unknown>): void {
  response.write(`event: ${value.type}\ndata: ${JSON.stringify(value)}\n\n`)
}

function writeJSON(response: ServerResponse, status: number, value: unknown, step?: MockModelStep): void {
  writeMockJSON(response, status, value, rateLimitHeaders(step))
}
