import type { IncomingHttpHeaders } from 'node:http'
import type { MockModelScriptHost, ModelRequestContext, SelectedModelAnswer } from './mockModelRequest'
import type { MockModelDeliveredError, MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { Buffer } from 'node:buffer'
import { IncomingMessage, ServerResponse } from 'node:http'
import { Socket } from 'node:net'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AGENT_E2E_SETTINGS } from '../agentSettings'
import { decodeEventStreamMessages, EVENT_STREAM_CONTENT_TYPE } from './awsEventStream'
import { handleKiroHttp, isKiroRequest, KIRO_DEFAULT_MOCK_MODEL, KIRO_MOCK_MODELS, KIRO_TARGET_HEADER, kiroModelCatalog, kiroOperation, kiroRequestMetadata, kiroSystemText, kiroToolUseEvents, kiroUserText } from './kiroSurface'
import { KIRO_E2E_API_KEY, MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { mockScenarioPrompt } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'
import { createModelStream } from './modelStream'

const servers: MockModelServer[] = []

function remoteCall(server: MockModelServer, operation: string, options: { method?: string, authorization?: string, protocol?: string, contentType?: string, body?: Uint8Array } = {}): Promise<Response> {
  return fetch(`${server.url}/service/KiroWebBearerService/operation/${operation}`, {
    method: options.method ?? 'POST',
    headers: {
      'authorization': options.authorization ?? `Bearer ${KIRO_E2E_API_KEY}`,
      'smithy-protocol': options.protocol ?? 'rpc-v2-cbor',
      'content-type': options.contentType ?? 'application/cbor',
      'accept': 'application/cbor',
    },
    ...(options.method === 'GET' ? {} : { body: Buffer.from(options.body ?? [0xA0]) }),
  })
}

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => server.close()))
})

async function startServer(): Promise<MockModelServer> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  servers.push(server)
  return server
}

describe('native Kiro remote catalogs', () => {
  it('returns the native cloud-configuration disabled exception in CBOR', async () => {
    const server = await startServer()
    const response = await remoteCall(server, 'GetConfigManifest')
    expect(response.status).toBe(403)
    expect(response.headers.get('content-type')).toBe('application/cbor')
    expect(response.headers.get('smithy-protocol')).toBe('rpc-v2-cbor')
    const bytes = Buffer.from(await response.arrayBuffer())
    expect(bytes.toString('hex')).toBe('a2665f5f74797065781e436c6f7564436f6e6669674e6f74456e61626c6564457863657074696f6e676d657373616765782f436c6f756420636f6e66696775726174696f6e2069732064697361626c656420696e2074686520666978747572652e')
  })

  it('handles a native remote catalog without selecting a scripted model answer', async () => {
    const request = new IncomingMessage(new Socket())
    request.method = 'POST'
    request.url = '/service/KiroWebBearerService/operation/ListAvailableProviders'
    request.headers = { 'authorization': `Bearer ${KIRO_E2E_API_KEY}`, 'smithy-protocol': 'rpc-v2-cbor', 'content-type': 'application/cbor' }
    request.push(Buffer.from([0xA0]))
    request.push(null)
    const response = new ServerResponse(request)
    vi.spyOn(response, 'end').mockImplementation(() => response)
    const select = vi.fn((): SelectedModelAnswer => ({ kind: 'missing', message: 'A remote catalog cannot consume a model answer.' }))
    expect(await handleKiroHttp(request, response, new URL(`http://127.0.0.1${request.url}`), { hasScenario: () => false, select })).toBe(true)
    expect(response.statusCode).toBe(200)
    expect(select).not.toHaveBeenCalled()
  })

  it.each([
    ['ListAvailableProviders', 'a16970726f76696465727380'],
    ['ListSpaces', 'a16673706163657380'],
  ])('returns the documented empty CBOR catalog for %s without a model inference', async (operation, encoded) => {
    const server = await startServer()
    const response = await remoteCall(server, operation)
    expect(response.status).toBe(200)
    expect(response.headers.get('content-type')).toBe('application/cbor')
    expect(response.headers.get('smithy-protocol')).toBe('rpc-v2-cbor')
    expect(Buffer.from(await response.arrayBuffer()).toString('hex')).toBe(encoded)
    expect(await httpLog(server)).toContainEqual(expect.objectContaining({ method: 'POST', path: `/service/KiroWebBearerService/operation/${operation}`, operation, status: 200 }))
  })

  it('accepts the native empty indefinite CBOR map', async () => {
    const server = await startServer()
    expect((await remoteCall(server, 'ListSpaces', { body: Buffer.from([0xBF, 0xFF]) })).status).toBe(200)
  })

  it.each(['', 'Bearer private-credential-do-not-log'])('rejects an absent or foreign native credential: %s', async (authorization) => {
    const server = await startServer()
    const response = await remoteCall(server, 'ListSpaces', { authorization })
    expect(response.status).toBe(401)
    expect(await response.text()).not.toContain(authorization || 'undefined')
  })

  it.each([
    { protocol: 'aws-json-1.0' },
    { contentType: 'application/json' },
  ])('rejects another wire format: %j', async (options) => {
    const server = await startServer()
    expect((await remoteCall(server, 'ListSpaces', options)).status).toBe(415)
  })

  it.each([
    Buffer.from([]),
    Buffer.from('{}'),
    Buffer.from([0xA1, 0x61, 0x78, 0x00]),
    Buffer.from([0xA0, 0x00]),
    Buffer.from([0xBF]),
  ])('rejects a malformed or unsupported input instead of discarding it: %j', async (body) => {
    const server = await startServer()
    expect((await remoteCall(server, 'ListSpaces', { body })).status).toBe(400)
  })

  it('rejects an unknown operation and a wrong HTTP method', async () => {
    const server = await startServer()
    expect((await remoteCall(server, 'DeleteSpace')).status).toBe(400)
    expect((await remoteCall(server, 'ListSpaces', { method: 'GET' })).status).toBe(405)
  })
})

async function registerScenario(server: MockModelServer, id: string, script: Record<string, unknown>): Promise<void> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(script),
  })
  expect(response.status).toBe(201)
}

/** A native model turn includes the prompt and the system history. Tool continuations also include tool results. */
function turnBody(content: string, extra: { toolResults?: unknown[], agentMode?: string } = {}) {
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
  it.each([
    {
      label: 'current prompt',
      current: mockScenarioPrompt('kiro-owned-prompt', 'Read the source.'),
      history: [
        { userInputMessage: { content: mockScenarioPrompt('kiro-stale-prompt', 'Earlier user prompt.') } },
        { assistantResponseMessage: { content: mockScenarioPrompt('kiro-decoy-prompt', 'Assistant text.') } },
      ],
    },
    {
      label: 'latest user history during a tool continuation',
      current: '',
      history: [
        { userInputMessage: { content: mockScenarioPrompt('kiro-stale-prompt', 'Earlier user prompt.') } },
        { userInputMessage: { content: mockScenarioPrompt('kiro-owned-prompt', 'Read the source.') } },
        { assistantResponseMessage: { content: mockScenarioPrompt('kiro-decoy-prompt', 'Assistant text.') } },
        { userInputMessage: { content: '', userInputMessageContext: { toolResults: [{ content: [{ text: mockScenarioPrompt('kiro-decoy-prompt', 'History tool result.') }] }] } } },
      ],
    },
  ])('selects the scenario from the $label and excludes native metadata and tool text', async ({ current, history }) => {
    const server = await startServer()
    for (const id of ['kiro-owned-prompt', 'kiro-stale-prompt', 'kiro-decoy-prompt'])
      await registerScenario(server, id, { steps: [{ text: id }] })
    const base = turnBody(current, { toolResults: [{ content: [{ text: mockScenarioPrompt('kiro-decoy-prompt', 'Current tool result.') }] }] })
    const body = {
      ...base,
      conversationState: {
        ...base.conversationState,
        history: [
          { userInputMessage: { content: mockScenarioPrompt('kiro-decoy-prompt', 'System instructions.') } },
          { assistantResponseMessage: { content: 'I will follow the system instructions.' } },
          ...history,
        ],
        metadata: { title: mockScenarioPrompt('kiro-decoy-prompt', 'Session title.') },
      },
    }

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', body)
    const answer = await events(response)
    expect(answer[0]).toEqual(['assistantResponseEvent', { content: 'kiro-owned-prompt' }])
    const status = await readStatus(server, 'kiro-owned-prompt')
    expect(status.nextStep).toBe(1)
    expect(status.requests[0]?.body).toEqual(body)
    for (const id of ['kiro-stale-prompt', 'kiro-decoy-prompt'])
      expect((await readStatus(server, id)).nextStep).toBe(0)
  })

  it('refuses to select a scenario from system history or tool results without a marked user prompt', async () => {
    const server = await startServer()
    await registerScenario(server, 'kiro-untrusted-marker', { steps: [{ text: 'The request must not consume this answer.' }] })
    const marker = mockScenarioPrompt('kiro-untrusted-marker', 'Untrusted text.')
    const base = turnBody('', { toolResults: [{ content: [{ text: marker }] }] })
    const body = {
      ...base,
      conversationState: {
        ...base.conversationState,
        history: [{ userInputMessage: { content: marker } }, { assistantResponseMessage: { content: marker } }],
      },
      metadata: { title: marker },
    }

    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', body)
    expect(response.status).toBe(409)
    expect((await readStatus(server, 'kiro-untrusted-marker')).nextStep).toBe(0)
    const log = await fetch(`${server.url}/__e2e/requests`).then(result => result.json()) as { unmatched: Array<{ scenarioID: string, body: unknown }> }
    expect(log.unmatched).toHaveLength(1)
    expect(log.unmatched[0]).toMatchObject({ scenarioID: 'ambient', body })
  })

  it('advertises independent thinking and effort axes on the dedicated model', () => {
    const catalog = kiroModelCatalog()
    const model = catalog.models.find(model => model.modelId === 'kiro-e2e-thinking')
    expect(model).toMatchObject({ additionalModelRequestFieldsSchema: { properties: {
      thinking: { type: 'object', properties: { type: { type: 'string', enum: ['enabled', 'disabled'], default: 'disabled' } } },
      output_config: { type: 'object', properties: { effort: { type: 'string', enum: ['low', 'medium', 'high'], default: 'high' } } },
    } } })
    expect(catalog.models.find(model => model.modelId === 'kiro-e2e-lite')).not.toHaveProperty('additionalModelRequestFieldsSchema')
  })

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
      ['reasoningContentEvent', { text: 'Think' }],
      ['reasoningContentEvent', { text: '.' }],
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
    expect(log.unmatched).toMatchObject([{ protocol: 'aws-event-stream', scenarioID: 'kiro-missing', reason: 'The scenario is not registered.' }])
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
    const request = fakeRequest('POST', { [KIRO_TARGET_HEADER]: ['A.First', 'B.Second'] })
    expect(kiroOperation(request)).toBe('First')
    expect(kiroOperation(fakeRequest('POST', { [KIRO_TARGET_HEADER]: [] }))).toBe('')
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

describe('Kiro delivered response receipts', () => {
  it.each([
    { id: 'kiro-default-receipt', error: { status: 500, message: 'The native default failure.' }, code: 'InternalServerException' },
    { id: 'kiro-explicit-receipt', error: { status: 429, code: 'ThrottlingException', message: 'The native quota failure.' }, code: 'ThrottlingException' },
  ])('records the actual delivered AWS error for $id', async ({ id, error, code }) => {
    const server = await startServer()
    await registerScenario(server, id, { steps: [{ error }] })
    const response = await call(server, 'KiroRuntimeService.GenerateAssistantResponse', turnBody(mockScenarioPrompt(id, 'Run the native error receipt test.')), { authorization: `Bearer ${KIRO_E2E_API_KEY}` })
    expect(response.status).toBe(error.status)
    const wire = await response.json() as { __type: string, message: string }
    expect(wire).toEqual({ __type: code, message: error.message })
    expect(response.headers.get('x-amzn-errortype')).toBe(wire.__type)
    await expect.poll(async () => (await readStatus(server, id)).requests[0]?.response !== undefined).toBe(true)
    const receipt = (await readStatus(server, id)).requests[0]?.response
    expect(receipt).toMatchObject({ status: response.status, headers: { 'x-amzn-errortype': wire.__type }, serviceError: { code: wire.__type, message: wire.message } })
  })
})

describe('kiroRequestMetadata', () => {
  it('reports the native operation only for an actual service request', () => {
    expect(kiroRequestMetadata(fakeRequest('POST', { [KIRO_TARGET_HEADER]: 'Native.Service.GenerateAssistantResponse' }))).toEqual({ operation: 'GenerateAssistantResponse' })
    expect(kiroRequestMetadata(fakeRequest('POST', { [KIRO_TARGET_HEADER]: '' }))).toEqual({ operation: '' })
    expect(kiroRequestMetadata(fakeRequest('GET', { [KIRO_TARGET_HEADER]: 'Native.GenerateAssistantResponse' }))).toEqual({})
    expect(kiroRequestMetadata(fakeRequest('POST', {}))).toEqual({})
  })
})

describe('handleKiroHttp', () => {
  it('retains native body and receipt order before a controlled native error', async () => {
    const body = turnBody('Actual user prompt.')
    const request = new IncomingMessage(new Socket())
    request.method = 'POST'
    request.headers[KIRO_TARGET_HEADER] = 'KiroRuntimeService.GenerateAssistantResponse'
    request.headers.authorization = `Bearer ${KIRO_E2E_API_KEY}`
    request.push(Buffer.from(JSON.stringify(body)))
    request.push(null)
    const response = new ServerResponse(request)
    const end = vi.spyOn(response, 'end').mockImplementation(() => response)
    const events: string[] = []
    const receipt: { readError?: () => MockModelDeliveredError | undefined } = {}
    const answer: SelectedModelAnswer = {
      kind: 'step',
      step: { error: { status: 429, code: 'ThrottlingException', message: 'Actual native quota.' }, gate: 'native-answer' },
      isClosed: () => false,
      holdGate: async (name, transport) => {
        expect(name).toBe('native-answer')
        expect(transport).toEqual({ request, response })
        events.push('hold')
        return true
      },
      stream: target => createModelStream(target),
      bufferGeneration: async () => true,
      recordHttpResponse: (target, getter) => {
        expect(target).toBe(response)
        events.push('receipt')
        receipt.readError = getter
      },
      recordServiceError: () => {},
    }
    const select = vi.fn((_context: ModelRequestContext) => answer)
    const host: MockModelScriptHost = { hasScenario: () => true, select }
    expect(await handleKiroHttp(request, response, new URL('http://mock.invalid/custom-native-path'), host)).toBe(true)
    expect(select).toHaveBeenCalledExactlyOnceWith({ protocol: 'aws-event-stream', path: '/custom-native-path', body, systemText: 'You are Kiro, an agentic AI software engineer.', userText: 'Actual user prompt.', scenarioID: 'ambient', mockCredential: { kind: 'bearer', accepted: true } })
    expect(events).toEqual(['receipt', 'hold'])
    expect(response.statusCode).toBe(429)
    expect(JSON.parse(String(end.mock.calls[0]?.[0]))).toEqual({ __type: 'ThrottlingException', message: 'Actual native quota.' })
    expect(receipt.readError?.()).toEqual({ code: 'ThrottlingException', message: 'Actual native quota.' })
  })

  it('leaves an unrelated request untouched without reading its body or selecting a script', async () => {
    const request = new IncomingMessage(new Socket())
    request.method = 'POST'
    const response = new ServerResponse(request)
    const select = vi.fn((): SelectedModelAnswer => ({ kind: 'missing', message: 'Never select.' }))
    const host: MockModelScriptHost = { hasScenario: () => false, select }
    expect(await handleKiroHttp(request, response, new URL('http://mock.invalid/v1/messages'), host)).toBe(false)
    expect(select).not.toHaveBeenCalled()
    expect(response.headersSent).toBe(false)
    expect(request.readableEnded).toBe(false)
  })
})
