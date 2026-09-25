import type { IncomingHttpHeaders, IncomingMessage } from 'node:http'
import type { MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { Buffer } from 'node:buffer'
import { afterEach, describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AGENT_E2E_SETTINGS } from '../agentSettings'
import { decodeEventStreamMessages, EVENT_STREAM_CONTENT_TYPE } from './awsEventStream'
import { isKiroRequest, KIRO_DEFAULT_MOCK_MODEL, KIRO_MOCK_MODELS, KIRO_TARGET_HEADER, kiroModelCatalog, kiroOperation, kiroSystemText, kiroToolUseEvents, kiroUserText } from './kiroSurface'
import { KIRO_E2E_API_KEY, MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { mockScenarioPrompt } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'

const servers: MockModelServer[] = []

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => server.close()))
})

async function startServer(): Promise<MockModelServer> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  servers.push(server)
  return server
}

async function registerScenario(server: MockModelServer, id: string, script: Record<string, unknown>): Promise<void> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(script),
  })
  expect(response.status).toBe(201)
}

/** A model turn as Kiro's engine sends it: the prompt, the system prompt in the history, and tool results. */
function turnBody(content: string, extra: { toolResults?: unknown[], agentMode?: string } = {}): Record<string, unknown> {
  return {
    conversationState: {
      conversationId: 'sess_1',
      history: [
        { userInputMessage: { content: 'You are Kiro, an agentic AI software engineer.', origin: 'AI_EDITOR' } },
        { assistantResponseMessage: { content: 'I will follow these instructions.' } },
      ],
      currentMessage: {
        userInputMessage: {
          content,
          origin: 'AI_EDITOR',
          modelId: 'kiro-e2e',
          ...(extra.toolResults ? { userInputMessageContext: { toolResults: extra.toolResults } } : {}),
        },
      },
    },
    agentMode: extra.agentMode ?? 'vibe',
  }
}

function call(server: MockModelServer, operation: string, body: unknown, init: { authorization?: string, signal?: AbortSignal } = {}): Promise<Response> {
  return fetch(`${server.url}/`, {
    method: 'POST',
    headers: {
      'content-type': 'application/x-amz-json-1.0',
      'x-amz-target': operation,
      ...(init.authorization !== undefined ? { authorization: init.authorization } : {}),
    },
    body: JSON.stringify(body),
    ...(init.signal ? { signal: init.signal } : {}),
  })
}

async function readStatus(server: MockModelServer, id: string): Promise<MockModelScenarioStatus> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`)
  expect(response.status).toBe(200)
  return await response.json() as MockModelScenarioStatus
}

/** Wait until the scenario consumed `count` steps, so no test sizes an interval. */
async function waitForStep(server: MockModelServer, id: string, count: number): Promise<void> {
  await expect.poll(async () => (await readStatus(server, id)).nextStep).toBeGreaterThanOrEqual(count)
}

/** The request log of the mock. */
async function httpLog(server: MockModelServer): Promise<Array<Record<string, unknown>>> {
  const log = await fetch(`${server.url}/__e2e/requests`).then(result => result.json()) as { http: Array<Record<string, unknown>> }
  return log.http
}

/** The events of one answer, as `[event type, payload]` pairs. */
async function events(response: Response): Promise<Array<[string, unknown]>> {
  expect(response.status).toBe(200)
  expect(response.headers.get('content-type')).toBe(EVENT_STREAM_CONTENT_TYPE)
  const messages = decodeEventStreamMessages(Buffer.from(await response.arrayBuffer()))
  return messages.map(message => [message.headers[':event-type'] ?? '', JSON.parse(message.payload.toString('utf8'))])
}

describe('the Kiro surface of the mock', () => {
  it('answers the model catalogue with each model and its effort axis', async () => {
    const server = await startServer()
    const response = await call(server, 'KiroControlPlaneBearerService.ListAvailableModels', { origin: 'AI_EDITOR' })
    expect(response.status).toBe(200)
    const catalog = await response.json() as { models: Array<Record<string, unknown>>, defaultModel: Record<string, unknown> }
    expect(catalog.models.map(model => model.modelId)).toEqual(KIRO_MOCK_MODELS.map(model => model.modelId))
    expect(catalog.defaultModel.modelId).toBe('kiro-e2e')
    expect(catalog.models[0]?.additionalModelRequestFieldsSchema).toEqual({
      type: 'object',
      properties: { output_config: { type: 'object', properties: { effort: { type: 'string', enum: ['low', 'medium', 'high'], default: 'high' } } } },
    })
    expect(catalog.models[1]).not.toHaveProperty('additionalModelRequestFieldsSchema')
  })

  it('streams a scripted turn and ends it with the stop reason', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-text', { steps: [{ reasoning: 'Think.', text: 'Hello there', stream: { chunkChars: 5, delayMs: 0 } }] })

    const answer = await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-text', 'Say hi.'))))

    expect(answer).toEqual([
      ['reasoningContentEvent', { text: 'Think.' }],
      ['assistantResponseEvent', { content: 'Hello' }],
      ['assistantResponseEvent', { content: ' ther' }],
      ['assistantResponseEvent', { content: 'e' }],
      ['metadataEvent', { tokenUsage: { uncachedInputTokens: 1, outputTokens: 1, totalTokens: 2 }, stopReason: 'END_TURN' }],
    ])
  })

  it('streams a tool call and ends the turn for the tool', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-tool', { steps: [{ toolCalls: [{ id: 't1', name: 'read_file', arguments: { path: '/w/a.txt' } }] }] })

    const answer = await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-tool', 'Read.'))))

    expect(answer).toEqual([
      ['toolUseEvent', { toolUseId: 't1', name: 'read_file', input: '{"path":"/w/a.txt"}' }],
      ['toolUseEvent', { toolUseId: 't1', name: 'read_file', stop: true }],
      ['metadataEvent', expect.objectContaining({ stopReason: 'TOOL_USE' })],
    ])
  })

  // Kiro reads the text of a turn before its tool calls, as the model wrote them.
  it('streams the text of a step before its tool calls', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-both', { steps: [{ text: 'Reading.', toolCalls: [{ id: 't1', name: 'read_file', arguments: {} }] }] })

    const answer = await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-both', 'Read.'))))

    expect(answer.map(([type]) => type)).toEqual(['assistantResponseEvent', 'toolUseEvent', 'toolUseEvent', 'metadataEvent'])
    expect(answer[0]).toEqual(['assistantResponseEvent', { content: 'Reading.' }])
    expect(answer.at(-1)).toEqual(['metadataEvent', expect.objectContaining({ stopReason: 'TOOL_USE' })])
  })

  it('matches a rule on the user text and the tool results', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-rule', {
      rules: [{ name: 'after-tool', when: { protocol: 'aws-event-stream', user: 'TOOL-OUTPUT-42' }, respond: { text: 'Saw the output.' } }],
    })

    const body = turnBody(`\n\n${mockScenarioPrompt('kiro-rule', '')}`, { toolResults: [{ toolUseId: 't1', content: [{ text: 'TOOL-OUTPUT-42' }], status: 'success' }] })
    const answer = await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', body))

    expect(answer[0]).toEqual(['assistantResponseEvent', { content: 'Saw the output.' }])
  })

  it('echoes the conversation id of the turn in both conversation headers', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-conversation', { steps: [{ text: 'One.' }, { text: 'Two.' }] })

    const stated = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-conversation', 'First.')))
    expect(stated.headers.get('x-amzn-codewhisperer-conversation-id')).toBe('sess_1')
    expect(stated.headers.get('x-amzn-kiro-conversation-id')).toBe('sess_1')
    await stated.arrayBuffer()

    // A turn that states no conversation id gets a fresh one, the same in both headers.
    const body = turnBody(mockScenarioPrompt('kiro-conversation', 'Second.')) as { conversationState: Record<string, unknown> }
    delete body.conversationState.conversationId
    const fresh = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', body)
    const id = fresh.headers.get('x-amzn-codewhisperer-conversation-id')
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/)
    expect(fresh.headers.get('x-amzn-kiro-conversation-id')).toBe(id)
    await fresh.arrayBuffer()
  })

  it('states the internal server error type for a scripted error with no code', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-uncoded', { steps: [{ error: { status: 500, message: 'Boom' } }] })

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-uncoded', 'Fail.')))

    expect(response.status).toBe(500)
    expect(response.headers.get('x-amzn-errortype')).toBe('InternalServerException')
    expect(await response.json()).toEqual({ __type: 'InternalServerException', message: 'Boom' })
  })

  it('answers a scripted error in the AWS error shape', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-error', { steps: [{ error: { status: 429, code: 'ThrottlingException', message: 'Too many requests' } }] })

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-error', 'Fail.')))

    expect(response.status).toBe(429)
    expect(response.headers.get('x-amzn-errortype')).toBe('ThrottlingException')
    expect(await response.json()).toEqual({ __type: 'ThrottlingException', message: 'Too many requests' })
  })

  it('refuses a turn that no scenario answers, and records it', async () => {
    const server = await startServer()
    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-missing', 'Nobody.')))

    expect(response.status).toBe(409)
    expect(response.headers.get('x-amzn-errortype')).toBe('ConflictException')
    const log = await fetch(`${server.url}/__e2e/requests`).then(result => result.json())
    expect(log.unmatched).toMatchObject([{ protocol: 'aws-event-stream', scenarioID: 'kiro-missing', reason: 'the scenario is not registered' }])
  })

  it('answers every other operation with a validation error, which Kiro takes as a default', async () => {
    const server = await startServer()
    const response = await call(server, 'KiroRuntimeService.GetFeatureConfiguration', { origin: 'AI_EDITOR', version: '0.0.1' })
    expect(response.status).toBe(400)
    expect(await response.json()).toMatchObject({ __type: 'ValidationException' })
  })

  it('records the operation of each call, which the path of every call leaves out', async () => {
    const server = await startServer()
    await call(server, 'KiroRuntimeService.GetFeatureConfiguration', {})
    expect(await httpLog(server)).toContainEqual({ method: 'POST', path: '/', operation: 'GetFeatureConfiguration', status: 400 })
  })

  it('takes the API key of the E2E environment, and a call that states no key', async () => {
    const server = await startServer()
    expect((await call(server, 'KiroControlPlaneBearerService.ListAvailableModels', {}, { authorization: `Bearer ${KIRO_E2E_API_KEY}` })).status).toBe(200)
    expect((await call(server, 'KiroControlPlaneBearerService.ListAvailableModels', {})).status).toBe(200)
  })

  it('refuses a credential that the E2E environment did not set, and records it', async () => {
    // A real login, for example one that Kiro reads from the keychain, sends a
    // bearer of its own. The refusal makes that visible rather than silent.
    const server = await startServer()
    const response = await call(server, 'KiroControlPlaneBearerService.ListAvailableModels', {}, { authorization: 'Bearer aoa_real_login_token' })
    expect(response.status).toBe(401)
    expect(response.headers.get('x-amzn-errortype')).toBe('UnauthorizedException')
    expect(await httpLog(server)).toContainEqual({ method: 'POST', path: '/', operation: 'ListAvailableModels', status: 401 })
  })

  it('answers a step that Kiro\'s service cannot state with a validation error that Kiro reads', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-raw-input', { steps: [{ toolCalls: [{ id: 't1', name: 'exec', input: 'code' }] }] })

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-raw-input', 'Run.')))

    expect(response.status).toBe(400)
    expect(response.headers.get('x-amzn-errortype')).toBe('ValidationException')
    expect(await response.json()).toEqual({ __type: 'ValidationException', message: expect.stringContaining('no custom tool') })
  })

  it('refuses a turn after the queue ran out, in the AWS error shape', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-one', { steps: [{ text: 'Only once.' }] })
    await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-one', 'First.'))))

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-one', 'Second.')))

    expect(response.status).toBe(409)
    expect(response.headers.get('x-amzn-errortype')).toBe('ConflictException')
    expect(await response.json()).toMatchObject({ __type: 'ConflictException', message: expect.stringContaining('has no answer for this request') })
  })

  it('abandons a held turn when Kiro disconnects, and stays available', async () => {
    const server = await startServer()
    // Longer than this test can take. The abort ends it, not the timer.
    await registerScenario(server, 'kiro-held', { steps: [{ text: 'Never sent', delayMs: 60_000 }] })

    const controller = new AbortController()
    const aborted = call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-held', 'Cancel me.')), { signal: controller.signal })
    // The server records the step before it holds the response open, so this
    // waits for the hold itself rather than for an interval.
    await waitForStep(server, 'kiro-held', 1)
    controller.abort()
    await expect(aborted).rejects.toThrow()

    expect(await readStatus(server, 'kiro-held')).toMatchObject({ complete: true, nextStep: 1 })
    await registerScenario(server, 'kiro-after-abort', { steps: [{ text: 'Still serving' }] })
    const answer = await events(await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt('kiro-after-abort', 'Run.'))))
    expect(answer[0]).toEqual(['assistantResponseEvent', { content: 'Still serving' }])
  })
})

describe('kiroUserText', () => {
  it('reads the prompt and the text of each tool result', () => {
    expect(kiroUserText(turnBody('Prompt', { toolResults: [{ toolUseId: 't', content: [{ text: 'out' }] }, 'x'] }))).toBe('Prompt\nout')
  })

  it('reads nothing from another shape', () => {
    expect(kiroUserText({})).toBe('')
    expect(kiroUserText({ conversationState: { currentMessage: {} } })).toBe('')
  })
})

describe('kiroSystemText', () => {
  it('reads the first message of the history, where Kiro states its system prompt', () => {
    expect(kiroSystemText(turnBody('x'))).toBe('You are Kiro, an agentic AI software engineer.')
    expect(kiroSystemText({ conversationState: { history: [] } })).toBe('')
    expect(kiroSystemText(null)).toBe('')
  })
})

/** A request that holds only the fields that the surface reads. */
function fakeRequest(method: string, headers: IncomingHttpHeaders): IncomingMessage {
  return { method, headers } as IncomingMessage
}

describe('isKiroRequest', () => {
  it('takes a POST that states an operation, and nothing else', () => {
    expect(isKiroRequest(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'KiroRuntimeService.GenerateAssistantResponse' }))).toBe(true)
    expect(isKiroRequest(fakeRequest('GET', { [KIRO_TARGET_HEADER]: 'KiroRuntimeService.GenerateAssistantResponse' }))).toBe(false)
    expect(isKiroRequest(fakeRequest('POST', {}))).toBe(false)
  })
})

describe('kiroOperation', () => {
  it('reads the last segment of the target', () => {
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'KiroRuntimeService.GenerateAssistantResponse' }))).toBe('GenerateAssistantResponse')
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'a.b.ListAvailableModels' }))).toBe('ListAvailableModels')
  })

  it('reads a target with no service as the operation itself', () => {
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'ListAvailableModels' }))).toBe('ListAvailableModels')
  })

  it('reads an absent or empty target as no operation', () => {
    expect(kiroOperation(fakeRequest('POST', {}))).toBe('')
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: '' }))).toBe('')
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'Service.' }))).toBe('')
  })

  it('reads the first value of a repeated header', () => {
    const request = fakeRequest('POST', { [KIRO_TARGET_HEADER]: ['A.First', 'B.Second'] as unknown as string })
    expect(kiroOperation(request)).toBe('First')
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: [] as unknown as string }))).toBe('')
  })
})

describe('kiroToolUseEvents', () => {
  it('states the whole input in one event, then the event that closes the call', () => {
    expect(kiroToolUseEvents({ id: 't', name: 'read_file', arguments: { path: '/w/a' } })).toEqual([
      ['toolUseEvent', { toolUseId: 't', name: 'read_file', input: '{"path":"/w/a"}' }],
      ['toolUseEvent', { toolUseId: 't', name: 'read_file', stop: true }],
    ])
  })

  it('states an empty object for a call with no arguments', () => {
    expect(kiroToolUseEvents({ id: 't', name: 'x' })[0]).toEqual(['toolUseEvent', { toolUseId: 't', name: 'x', input: '{}' }])
  })

  it('refuses a call that Kiro\'s service cannot state', () => {
    expect(() => kiroToolUseEvents({ id: 't', name: 'exec', input: 'code' })).toThrow('no custom tool')
    expect(() => kiroToolUseEvents({ id: 't', name: 'x', arguments: {}, namespace: 'ns' })).toThrow('no tool namespace')
  })
})

describe('kiroModelCatalog', () => {
  // The E2E settings open each Kiro agent on this model, so the catalogue must
  // answer it as its default.
  it('answers the model that the E2E settings open as its default', () => {
    const catalog = kiroModelCatalog() as { defaultModel: Record<string, unknown> }
    expect(catalog.defaultModel.modelId).toBe(KIRO_DEFAULT_MOCK_MODEL.modelId)
    expect(AGENT_E2E_SETTINGS[AgentProvider.KIRO].model).toBe(KIRO_DEFAULT_MOCK_MODEL.modelId)
  })

  // `229-kiro-settings` reads the chip of the pinned effort. The model must offer
  // that effort, and the effort must differ from the model's own default, or a start
  // that dropped it would look the same.
  it('offers the pinned effort, which differs from the default effort of the model', () => {
    const effort = AGENT_E2E_SETTINGS[AgentProvider.KIRO].effort
    expect(KIRO_DEFAULT_MOCK_MODEL.effortLevels).toContain(effort)
    expect(effort).not.toBe(KIRO_DEFAULT_MOCK_MODEL.defaultEffort)
  })
})
