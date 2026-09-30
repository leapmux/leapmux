import type { MockModelScript } from './mockModelScenario'
import type { MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { Buffer } from 'node:buffer'
import { request as httpRequest } from 'node:http'
import { afterEach, describe, expect, it } from 'vitest'
import { CURSOR_RUN_PATH } from './cursorSurface'
import { connectFrame, encodeLengthDelimited, encodeStringField } from './cursorWire'
import { MOCK_MODEL_IDS, MOCK_MODELS, MODEL_KEY } from './mockAgentEnvironment'
import { mockScenarioPrompt } from './mockModelScenario'
import { AMBIENT_SCENARIO_ID } from './mockModelScript'
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

async function registerScenario(server: MockModelServer, id: string, script: MockModelScript): Promise<void> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(script),
  })
  expect(response.status).toBe(201)
}

async function readStatus(server: MockModelServer, id: string): Promise<MockModelScenarioStatus> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`)
  expect(response.status).toBe(200)
  return await response.json() as MockModelScenarioStatus
}

/** Wait until the scenario consumed `count` steps, so no test sizes an interval. */
async function waitForStep(server: MockModelServer, id: string, count: number): Promise<void> {
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    if ((await readStatus(server, id)).nextStep >= count)
      return
    await new Promise(resolve => setTimeout(resolve, 5))
  }
  throw new Error(`Model scenario ${id} did not reach step ${count}`)
}

/** Wait for a response gate to hold a real model request. */
async function waitForGate(server: MockModelServer, id: string, gate: string): Promise<MockModelScenarioStatus> {
  const deadline = Date.now() + 4_000
  while (Date.now() < deadline) {
    const status = await readStatus(server, id)
    if (status.pendingGates.includes(gate))
      return status
    await new Promise(resolve => setTimeout(resolve, 5))
  }
  throw new Error(`The model scenario ${id} never held gate ${gate}`)
}

async function responseText(response: Response): Promise<string> {
  expect(response.status).toBe(200)
  return response.text()
}

function chat(server: MockModelServer, content: string, stream = true): Promise<Response> {
  return fetch(`${server.url}/v1/chat/completions`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ model: 'mock-model', stream, messages: [{ role: 'user', content }] }),
  })
}

/** Send the native Cursor Run frame with the conversation that owns the prompt. */
async function cursorRun(server: MockModelServer, conversationID: string, prompt: string): Promise<void> {
  const action = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, encodeStringField(1, prompt))))
  const frame = encodeLengthDelimited(1, Buffer.concat([Buffer.from(action), Buffer.from(encodeStringField(5, conversationID))]))
  const response = await fetch(`${server.url}${CURSOR_RUN_PATH}`, {
    method: 'POST',
    headers: { 'content-type': 'application/connect+proto' },
    body: Buffer.from(connectFrame(frame)),
  })
  expect(response.status).toBe(200)
  await response.arrayBuffer()
}

describe('createMockModelServer', () => {
  it('routes a bare Cursor command through its conversation script and clears that route', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-conversation', { steps: [{ text: 'First answer.' }, { text: 'Command answer.' }] })
    await registerScenario(server, AMBIENT_SCENARIO_ID, { steps: [{ text: 'Ambient answer.' }] })
    await cursorRun(server, 'conversation-1', mockScenarioPrompt('cursor-conversation', 'Start this conversation.'))
    await cursorRun(server, 'conversation-1', '/compact')
    const scripted = await readStatus(server, 'cursor-conversation')
    expect(scripted.nextStep).toBe(2)
    expect(scripted.requests[1]?.body).toEqual({ prompt: '/compact', attachments: [], conversationId: 'conversation-1' })

    const removed = await fetch(`${server.url}/__e2e/scenarios/cursor-conversation`, { method: 'DELETE' })
    expect(removed.status).toBe(204)
    await cursorRun(server, 'conversation-1', '/compact')
    expect((await readStatus(server, AMBIENT_SCENARIO_ID)).nextStep).toBe(1)
  })

  it('refuses to start without a model identifier', async () => {
    await expect(createMockModelServer({ models: [] })).rejects.toThrow('at least one model identifier')
  })

  it('streams a scripted OpenAI chat completion and records its request', async () => {
    const server = await startServer()
    await registerScenario(server, 'chat-text', { steps: [{ text: 'Deterministic answer' }] })

    const body = await responseText(await chat(server, mockScenarioPrompt('chat-text', 'Answer the prompt.')))
    expect(body).toContain('Deterministic answer')
    expect(body).toContain('data: [DONE]')
    const chunks = body.split('\n')
      .filter(line => line.startsWith('data: {'))
      .map(line => JSON.parse(line.slice('data: '.length)))
    expect(chunks.at(-1)).toMatchObject({ choices: [], usage: { total_tokens: 2 } })

    const status = await readStatus(server, 'chat-text')
    expect(status).toMatchObject({ complete: true, nextStep: 1, stepCount: 1, unexpectedRequests: [] })
    expect(status.requests).toHaveLength(1)
    expect(status.requests[0]).toMatchObject({ protocol: 'openai-chat-completions', stepIndex: 0 })
  })

  it('holds a model answer until its gate is released', async () => {
    const server = await startServer()
    await registerScenario(server, 'held-answer', { steps: [{ text: 'Released answer.', gate: 'child-answer' }] })

    let answered = false
    const answer = chat(server, mockScenarioPrompt('held-answer', 'Ask the child.'), false)
      .then(async (response) => {
        answered = true
        return responseText(response)
      })
    const held = await waitForGate(server, 'held-answer', 'child-answer')
    expect(held).toMatchObject({ complete: false, nextStep: 1, stepCount: 1 })
    expect(answered).toBe(false)

    const release = await fetch(`${server.url}/__e2e/scenarios/held-answer/gates/child-answer/release`, { method: 'POST' })
    expect(release.status).toBe(204)
    expect(await answer).toContain('Released answer.')
    expect(await readStatus(server, 'held-answer')).toMatchObject({ complete: true, pendingGates: [] })
    const duplicate = await fetch(`${server.url}/__e2e/scenarios/held-answer/gates/child-answer/release`, { method: 'POST' })
    expect(duplicate.status).toBe(409)
  })

  it('cancels a held answer when the scenario is removed', async () => {
    const server = await startServer()
    await registerScenario(server, 'abandoned-gate', { steps: [{ text: 'Never sent.', gate: 'child-answer' }] })
    const pending = chat(server, mockScenarioPrompt('abandoned-gate', 'Ask the child.'), false)
    const rejection = expect(pending).rejects.toThrow()
    await waitForGate(server, 'abandoned-gate', 'child-answer')

    const normal = await fetch(`${server.url}/__e2e/scenarios/abandoned-gate`, { method: 'DELETE' })
    expect(normal.status).toBe(409)
    expect(await normal.json()).toMatchObject({ complete: false, pendingGates: ['child-answer'] })
    const forced = await fetch(`${server.url}/__e2e/scenarios/abandoned-gate?force=true`, { method: 'DELETE' })
    expect(forced.status).toBe(204)
    await rejection
  })

  it('advertises the pinned model identifiers and the client identity routes', async () => {
    const server = await startServer()
    const models = await fetch(`${server.url}/models`).then(response => response.json())
    expect(models.data.map((model: { id: string }) => model.id)).toEqual([...MOCK_MODEL_IDS])
    expect(models.data[0]).toMatchObject({
      capabilities: { supports: { vision: true }, limits: { max_context_window_tokens: 128000 } },
    })
    const copilotModel = models.data.find((model: { id: string }) => model.id === MOCK_MODELS.openai)
    expect(copilotModel).toMatchObject({
      capabilities: { supports: { reasoning_effort: ['none', 'low', 'medium', 'high', 'xhigh', 'max'] } },
    })
    expect(copilotModel.supported_endpoints).toBeUndefined()
    const reasoningModel = models.data.find((model: { id: string }) => model.id === MOCK_MODELS.gooseReasoning)
    expect(reasoningModel).toMatchObject({
      supported_endpoints: ['/responses'],
      capabilities: { supports: { reasoning_effort: ['low', 'medium', 'high'] } },
    })
    const piModel = models.data.find((model: { id: string }) => model.id === MOCK_MODELS.pi)
    expect(piModel.capabilities.supports).not.toHaveProperty('reasoning_effort')

    const user = await fetch(`${server.url}/copilot_internal/user`).then(response => response.json())
    expect(user).toMatchObject({
      login: 'leapmux-e2e',
      copilot_plan: 'individual_pro',
      endpoints: { api: server.url },
    })
    const githubUser = await fetch(`${server.url}/user`).then(response => response.json())
    expect(githubUser).toMatchObject({ id: 1, login: 'leapmux-e2e', type: 'User' })

    const automatic = await fetch(`${server.url}/auto`, { method: 'POST' }).then(response => response.json())
    expect(automatic).toMatchObject({
      session_token: 'leapmux-e2e-session-token',
      selected_model: { id: 'gpt-5.6-luna' },
    })
  })

  it('answers Droid whoami only for the isolated model key', async () => {
    const server = await startServer()
    await registerScenario(server, 'droid-whoami', { steps: [{ text: 'A model answer.' }] })
    const url = `${server.url}/v1/api/cli/whoami`

    const accepted = await fetch(url, { headers: { authorization: `Bearer ${MODEL_KEY}` } })
    expect(accepted.status).toBe(200)
    expect(await accepted.json()).toEqual({ userId: 'leapmux-e2e-user', orgId: 'leapmux-e2e-org' })
    expect((await fetch(url)).status).toBe(401)
    expect((await fetch(url, { headers: { authorization: 'Bearer another-key' } })).status).toBe(401)

    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.http).toContainEqual({ method: 'GET', path: '/v1/api/cli/whoami', status: 200 })
    expect((await readStatus(server, 'droid-whoami')).nextStep).toBe(0)
  })

  it('returns the endpoint shapes read by both Qoder discovery clients', async () => {
    const server = await startServer()
    const v3 = await fetch(`${server.url}/algo/api/v3/service/region/endpoints`).then(response => response.json())
    const legacyV5 = await fetch(`${server.url}/algo/api/v5/service/region/endpoints`, {
      headers: { 'cosy-machineid': 'mock-machine', 'x-gw-user-id': 'leapmux-e2e' },
    }).then(response => response.json())
    const newV5 = await fetch(`${server.url}/algo/api/v5/service/region/endpoints`, {
      headers: { 'cosy-machinecode': 'mock-code', 'cosy-machinetype': 'darwin', 'x-gw-user-id': 'leapmux-e2e' },
    }).then(response => response.json())

    for (const key of ['centerNodes', 'inferNodes', 'security', 'openapiNodes']) {
      expect(v3[key]).toEqual([server.url])
      expect(legacyV5[key]).toEqual([server.url])
      expect(newV5[key]).toEqual([{ url: server.url }])
    }
    const unknown = await fetch(`${server.url}/algo/api/v5/service/region/unknown`)
    expect(unknown.status).toBe(404)
  })

  it('keeps an HTTP log that a caller can read and clear', async () => {
    const server = await startServer()
    await fetch(`${server.url}/user`)
    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.http).toContainEqual(expect.objectContaining({ method: 'GET', path: '/user', status: 200 }))
    // The control routes stay out of the log, so a diagnosis reads agent traffic alone.
    expect(log.http.some((record: { path: string }) => record.path.startsWith('/__e2e/'))).toBe(false)

    expect((await fetch(`${server.url}/__e2e/requests`, { method: 'DELETE' })).status).toBe(204)
    expect(await fetch(`${server.url}/__e2e/requests`).then(response => response.json())).toEqual({ http: [], unmatched: [] })
  })

  it('streams OpenAI Responses text and tool calls', async () => {
    const server = await startServer()
    await registerScenario(server, 'responses-tools', {
      steps: [{ text: 'Inspecting the file.', toolCalls: [{ id: 'call-1', name: 'read_file', arguments: { path: 'README.md' } }] }],
    })

    const response = await fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'mock-model',
        stream: true,
        input: [{ role: 'user', content: [{ type: 'input_text', text: mockScenarioPrompt('responses-tools', 'Read the file.') }] }],
      }),
    })
    const body = await responseText(response)
    expect(body).toContain('response.output_item.done')
    expect(body).toContain('Inspecting the file.')
    expect(body).toContain('"type":"function_call"')
    expect(body).toContain('"name":"read_file"')
    expect(body).toContain('"arguments":"{\\"path\\":\\"README.md\\"}"')
    expect(body).toContain('response.completed')
  })

  it('streams complete OpenAI Responses reasoning events in sequence', async () => {
    const server = await startServer()
    await registerScenario(server, 'responses-reasoning', {
      steps: [
        { reasoning: 'Compare the values.', text: 'The answer is 6912.' },
        { reasoning: 'Compare the values.', text: 'The answer is 6912.' },
      ],
    })

    const body = await responseText(await fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'reasoning-model',
        stream: true,
        input: [{ role: 'user', content: [{ type: 'input_text', text: mockScenarioPrompt('responses-reasoning', 'Add two values.') }] }],
      }),
    }))
    const events = body.split('\n')
      .filter(line => line.startsWith('data: {'))
      .map(line => JSON.parse(line.slice('data: '.length)))

    expect(events.map(event => event.sequence_number)).toEqual(events.map((_, index) => index))
    expect(events[0]).toMatchObject({
      type: 'response.created',
      response: { id: expect.any(String), object: 'response', created_at: expect.any(Number), status: 'in_progress', model: 'reasoning-model', output: [], tools: [] },
    })
    const eventTypes = events.map(event => event.type)
    const added = eventTypes.indexOf('response.output_item.added')
    const delta = eventTypes.indexOf('response.reasoning_summary_text.delta')
    const done = eventTypes.indexOf('response.output_item.done')
    expect(added).toBeGreaterThan(0)
    expect(delta).toBeGreaterThan(added)
    expect(done).toBeGreaterThan(delta)
    expect(events[delta]).toMatchObject({ delta: 'Compare the values.', output_index: 0 })
    expect(events.at(-1)).toMatchObject({
      type: 'response.completed',
      response: { id: events[0].response.id, object: 'response', created_at: events[0].response.created_at, status: 'completed', model: 'reasoning-model', tools: [] },
    })

    const nonStreaming = await fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'reasoning-model',
        stream: false,
        input: [{ role: 'user', content: [{ type: 'input_text', text: mockScenarioPrompt('responses-reasoning', 'Add two values again.') }] }],
      }),
    })
    expect(await nonStreaming.json()).toMatchObject({ object: 'response', created_at: events[0].response.created_at, status: 'completed' })
  })

  it('streams Anthropic text and tool blocks', async () => {
    const server = await startServer()
    await registerScenario(server, 'anthropic-tools', {
      steps: [{ text: 'I will inspect it.', toolCalls: [{ id: 'tool-1', name: 'Read', arguments: { file_path: 'README.md' } }] }],
    })

    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: 'mock-model',
        stream: true,
        messages: [{ role: 'user', content: mockScenarioPrompt('anthropic-tools', 'Read the file.') }],
      }),
    })
    const body = await responseText(response)
    expect(body).toContain('event: message_start')
    expect(body).toContain('"type":"text_delta","text":"I will inspect it."')
    expect(body).toContain('"type":"tool_use","id":"tool-1","name":"Read","input":{}')
    expect(body).toContain('"type":"input_json_delta","partial_json":"{\\"file_path\\":\\"README.md\\"}"')
    expect(body).toContain('"stop_reason":"tool_use"')
    expect(body).toContain('event: message_stop')
  })

  it('reports reasoning in each protocol\'s own shape', async () => {
    const server = await startServer()
    const thinking = { reasoning: 'Weighing the options.', text: 'Done.' }
    await registerScenario(server, 'thinking', { steps: [thinking, thinking, thinking] })
    const prompt = mockScenarioPrompt('thinking', 'Think first.')

    expect(await responseText(await chat(server, prompt)))
      .toContain('"reasoning_content":"Weighing the options."')

    const responses = await responseText(await fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: true, input: [{ role: 'user', content: prompt }] }),
    }))
    // Codex reads both halves of its `Reasoning` item.
    expect(responses).toContain('"type":"reasoning"')
    expect(responses).toContain('"type":"summary_text","text":"Weighing the options."')
    expect(responses).toContain('"type":"reasoning_text"')

    const anthropic = await responseText(await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: true, messages: [{ role: 'user', content: prompt }] }),
    }))
    expect(anthropic).toContain('"type":"thinking_delta","thinking":"Weighing the options."')
    expect(anthropic).toContain('"type":"signature_delta"')
  })

  it('emits the Copilot OpenAI reasoning field for its native model', async () => {
    const server = await startServer()
    await registerScenario(server, 'copilot-reasoning', {
      steps: [
        { reasoning: 'Check the CAPI answer.', text: 'Done.' },
        { reasoning: 'Check the CAPI answer.', text: 'Done.' },
      ],
    })
    const response = await fetch(`${server.url}/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: MOCK_MODELS.openai,
        stream: true,
        messages: [{ role: 'user', content: mockScenarioPrompt('copilot-reasoning', 'Reply once.') }],
      }),
    })
    const body = await responseText(response)
    expect(body).toContain('"reasoning":"Check the CAPI answer."')

    const nonstreaming = await fetch(`${server.url}/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        model: MOCK_MODELS.openai,
        stream: false,
        messages: [{ role: 'user', content: mockScenarioPrompt('copilot-reasoning', 'Reply once more.') }],
      }),
    })
    expect(await nonstreaming.json()).toMatchObject({
      choices: [{ message: { reasoning: 'Check the CAPI answer.' } }],
    })
  })

  // A test that asserts context usage needs a count other than 1, and a test of
  // rate-limit state needs the headers a CLI parses. Both ride on one step.
  it('reports the step usage and rate-limit surface on every protocol', async () => {
    const server = await startServer()
    await registerScenario(server, 'usage-limits', {
      steps: [{
        text: 'Answered.',
        usage: { inputTokens: 12000, outputTokens: 40, contextWindow: 200000 },
        rateLimits: { type: 'five_hour', status: 'exceeded', utilization: 0.92, resetsAt: 1893456000 },
      }, {
        text: 'Answered.',
        usage: { inputTokens: 12000, outputTokens: 40, contextWindow: 200000 },
        rateLimits: { type: 'five_hour', status: 'exceeded', utilization: 0.92, resetsAt: 1893456000 },
      }, {
        text: 'Answered.',
        usage: { inputTokens: 12000, outputTokens: 40, contextWindow: 200000 },
        rateLimits: { type: 'five_hour', status: 'exceeded', utilization: 0.92, resetsAt: 1893456000 },
      }],
    })
    const prompt = mockScenarioPrompt('usage-limits', 'Answer once.')

    const completion = await chat(server, prompt)
    expect(completion.headers.get('x-leapmux-e2e-ratelimit-type')).toBe('five_hour')
    expect(completion.headers.get('x-leapmux-e2e-ratelimit-status')).toBe('exceeded')
    expect(completion.headers.get('x-leapmux-e2e-ratelimit-utilization')).toBe('0.92')
    expect(completion.headers.get('x-leapmux-e2e-ratelimit-resets-at')).toBe('1893456000')
    // The pair Claude Code reads before it emits `rate_limit_event`.
    expect(completion.headers.get('anthropic-ratelimit-unified-status')).toBe('exceeded')
    expect(completion.headers.get('anthropic-ratelimit-unified-representative-claim')).toBe('five_hour')
    expect(completion.headers.get('anthropic-ratelimit-unified-5h-utilization')).toBe('0.92')
    expect(completion.headers.get('anthropic-ratelimit-unified-5h-reset')).toBe('1893456000')
    expect(completion.headers.get('x-ratelimit-remaining-requests')).toBe('0')
    // The family Codex parses before it publishes `account/rateLimits/updated`.
    expect(completion.headers.get('x-codex-primary-used-percent')).toBe('92')
    expect(completion.headers.get('x-codex-primary-window-minutes')).toBe('300')
    expect(completion.headers.get('x-codex-primary-reset-at')).toBe('1893456000')
    expect(completion.headers.get('x-codex-limit-name')).toBe('five_hour')
    expect(completion.headers.get('x-codex-rate-limit-reached-type')).toBe('rate_limit_reached')
    expect(completion.headers.get('x-codex-secondary-used-percent')).toBeNull()
    const completionBody = await completion.text()
    expect(completionBody).toContain('"prompt_tokens":12000')
    expect(completionBody).toContain('"completion_tokens":40')
    expect(completionBody).toContain('"total_tokens":12040')

    const responses = await fetch(`${server.url}/v1/responses`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: true, input: [{ role: 'user', content: prompt }] }),
    })
    expect(responses.headers.get('anthropic-ratelimit-unified-status')).toBe('exceeded')
    const responsesBody = await responses.text()
    expect(responsesBody).toContain('"input_tokens":12000')
    expect(responsesBody).toContain('"output_tokens":40')
    expect(responsesBody).toContain('"total_tokens":12040')

    const anthropic = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: true, messages: [{ role: 'user', content: prompt }] }),
    })
    expect(anthropic.headers.get('anthropic-ratelimit-unified-status')).toBe('exceeded')
    expect(anthropic.headers.get('anthropic-ratelimit-unified-5h-utilization')).toBe('0.92')
    const anthropicBody = await anthropic.text()
    expect(anthropicBody).toContain('"input_tokens":12000')
    expect(anthropicBody).toContain('"output_tokens":40')
  })

  // A `seven_day` step rides Codex's secondary window, not its primary one.
  it('maps a seven_day rate-limit step onto the Codex secondary window', async () => {
    const server = await startServer()
    await registerScenario(server, 'weekly-limits', {
      steps: [{
        text: 'Answered.',
        rateLimits: { type: 'seven_day', status: 'allowed_warning', utilization: 0.81, resetsAt: 1894000000 },
      }],
    })
    const prompt = mockScenarioPrompt('weekly-limits', 'Answer once.')
    const completion = await chat(server, prompt)
    expect(completion.headers.get('x-codex-secondary-used-percent')).toBe('81')
    expect(completion.headers.get('x-codex-secondary-window-minutes')).toBe('10080')
    expect(completion.headers.get('x-codex-secondary-reset-at')).toBe('1894000000')
    expect(completion.headers.get('x-codex-primary-used-percent')).toBeNull()
    expect(completion.headers.get('x-codex-rate-limit-reached-type')).toBeNull()
    await completion.text()
  })

  // Every model API defaults `stream` to false. A client that omits the flag
  // reads one JSON body, and an event stream in reply fails to parse.
  it('answers a request that states no stream flag with one JSON body in each protocol', async () => {
    const server = await startServer()
    const answer = { text: 'One body.' }
    await registerScenario(server, 'unstreamed', { steps: [answer, answer, answer] })
    const prompt = mockScenarioPrompt('unstreamed', 'Answer once.')
    const post = (path: string, body: Record<string, unknown>) => fetch(`${server.url}${path}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })

    const chatResponse = await post('/v1/chat/completions', { messages: [{ role: 'user', content: prompt }] })
    expect(chatResponse.headers.get('content-type')).toContain('application/json')
    expect((await chatResponse.json()).choices[0].message.content).toBe('One body.')

    const responsesResponse = await post('/v1/responses', { input: [{ role: 'user', content: prompt }] })
    expect(responsesResponse.headers.get('content-type')).toContain('application/json')
    expect(await responsesResponse.json()).toMatchObject({ object: 'response', status: 'completed' })

    const messagesResponse = await post('/v1/messages', { messages: [{ role: 'user', content: prompt }] })
    expect(messagesResponse.headers.get('content-type')).toContain('application/json')
    expect((await messagesResponse.json()).content).toEqual([{ type: 'text', text: 'One body.' }])

    expect(await readStatus(server, 'unstreamed')).toMatchObject({ complete: true })
  })

  it('returns a scripted error with the protocol\'s own error shape', async () => {
    const server = await startServer()
    await registerScenario(server, 'rate-limited', {
      steps: [{ error: { status: 429, message: 'Slow down', code: 'rate_limit_exceeded' } }],
    })

    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ messages: [{ role: 'user', content: mockScenarioPrompt('rate-limited', 'Run.') }] }),
    })
    expect(response.status).toBe(429)
    expect(await response.json()).toEqual({
      type: 'error',
      error: { type: 'rate_limit_exceeded', message: 'Slow down' },
    })
  })

  it('keeps concurrent scenario queues independent', async () => {
    const server = await startServer()
    await registerScenario(server, 'first', { steps: [{ text: 'First response' }] })
    await registerScenario(server, 'second', { steps: [{ text: 'Second response' }] })

    const [second, first] = await Promise.all([
      chat(server, mockScenarioPrompt('second', 'Run.')).then(responseText),
      chat(server, mockScenarioPrompt('first', 'Run.')).then(responseText),
    ])

    expect(first).toContain('First response')
    expect(first).not.toContain('Second response')
    expect(second).toContain('Second response')
    expect(second).not.toContain('First response')
  })

  it('routes a conversation that carries two markers to the newest one', async () => {
    const server = await startServer()
    await registerScenario(server, 'older', { steps: [{ text: 'Older answer' }] })
    await registerScenario(server, 'newer', { steps: [{ text: 'Newer answer' }] })

    const response = await fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        stream: false,
        messages: [
          { role: 'user', content: mockScenarioPrompt('older', 'The first turn.') },
          { role: 'assistant', content: 'Older answer' },
          { role: 'user', content: mockScenarioPrompt('newer', 'The second turn.') },
        ],
      }),
    })
    expect((await response.json()).choices[0].message.content).toBe('Newer answer')
    expect(await readStatus(server, 'older')).toMatchObject({ nextStep: 0 })
    expect(await readStatus(server, 'newer')).toMatchObject({ nextStep: 1 })
  })

  it('answers through a matching rule without consuming a step', async () => {
    const server = await startServer()
    await registerScenario(server, 'ruled', {
      steps: [{ text: 'Primary answer' }],
      rules: [{ name: 'summary', when: { system: 'summari[sz]e the conversation' }, respond: { text: 'A summary.' } }],
    })
    const request = (messages: Array<Record<string, string>>) => fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: false, messages }),
    }).then(async (response) => {
      expect(response.status).toBe(200)
      return response.json()
    })

    const marked = mockScenarioPrompt('ruled', 'Inspect the parser.')
    const summary = await request([
      { role: 'system', content: 'Summarize the conversation so far.' },
      { role: 'user', content: marked },
    ])
    expect(summary.choices[0].message.content).toBe('A summary.')
    // A repeatable rule answers again.
    await request([{ role: 'system', content: 'Summarize the conversation so far.' }, { role: 'user', content: marked }])
    const answer = await request([{ role: 'user', content: marked }])
    expect(answer.choices[0].message.content).toBe('Primary answer')

    const status = await readStatus(server, 'ruled')
    expect(status).toMatchObject({ complete: true, nextStep: 1, stepCount: 1, ruleMatches: { summary: 2 } })
    expect(status.requests).toMatchObject([{ rule: 'summary' }, { rule: 'summary' }, { stepIndex: 0 }])
  })

  it('stops answering from a rule marked once', async () => {
    const server = await startServer()
    await registerScenario(server, 'once', {
      steps: [{ text: 'Queued answer' }],
      rules: [{ name: 'greeting', when: { user: 'hello' }, respond: { text: 'Rule answer' }, once: true }],
    })
    const prompt = mockScenarioPrompt('once', 'Hello there.')
    expect(await responseText(await chat(server, prompt))).toContain('Rule answer')
    expect(await responseText(await chat(server, prompt))).toContain('Queued answer')
    expect(await readStatus(server, 'once')).toMatchObject({ complete: true, ruleMatches: { greeting: 1 } })
  })

  it('takes the first rule that matches, so a test rule wins over a later one', async () => {
    const server = await startServer()
    await registerScenario(server, 'ordered', {
      rules: [
        { name: 'specific', when: { user: ['review', 'parser'] }, respond: { text: 'Specific' } },
        { name: 'general', when: { user: 'review' }, respond: { text: 'General' } },
      ],
    })
    expect(await responseText(await chat(server, mockScenarioPrompt('ordered', 'Review the parser.')))).toContain('Specific')
    expect(await responseText(await chat(server, mockScenarioPrompt('ordered', 'Review the build.')))).toContain('General')
  })

  it('records an exhausted scenario and never borrows another one', async () => {
    const server = await startServer()
    await registerScenario(server, 'one-step', { steps: [{ text: 'Only response' }] })
    await registerScenario(server, 'spare', { steps: [{ text: 'Spare response' }] })

    expect((await chat(server, mockScenarioPrompt('one-step', 'Consume it.'))).status).toBe(200)
    expect((await chat(server, mockScenarioPrompt('one-step', 'Consume it again.'))).status).toBe(409)
    expect(await readStatus(server, 'spare')).toMatchObject({ nextStep: 0 })

    const status = await readStatus(server, 'one-step')
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toHaveLength(1)
    expect(status.unexpectedRequests[0]).toMatchObject({
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      reason: 'scenario exhausted',
      body: { messages: [{ role: 'user', content: mockScenarioPrompt('one-step', 'Consume it again.') }] },
    })
  })

  it('fills a captured request value into the tool call it answers with', async () => {
    const server = await startServer()
    await registerScenario(server, 'captured', {
      steps: [
        {
          toolCalls: [{ id: 'write-plan', name: 'Write', arguments: { path: '{{planFile}}', content: '# Plan' } }],
          captures: { planFile: 'Plan file: (\\S+\\.md)' },
        },
        { text: 'Unused', captures: { planFile: 'Plan file: (\\S+\\.md)' }, reasoning: '{{planFile}}' },
      ],
    })
    const response = await fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        stream: false,
        messages: [
          { role: 'user', content: mockScenarioPrompt('captured', 'Make a plan.') },
          { role: 'user', content: '<system-reminder>Plan file: /tmp/plans/red-fox.md</system-reminder>' },
        ],
      }),
    })
    expect(response.status).toBe(200)
    const answer = await response.json()
    expect(JSON.parse(answer.choices[0].message.tool_calls[0].function.arguments))
      .toEqual({ path: '/tmp/plans/red-fox.md', content: '# Plan' })

    // The second step's capture finds no plan path, so the request is unexpected.
    expect((await chat(server, mockScenarioPrompt('captured', 'No reminder.'))).status).toBe(409)
    const status = await readStatus(server, 'captured')
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toMatchObject([{ reason: 'capture planFile matched nothing in the request' }])
  })

  it('records a request that reaches no scenario, with the body that arrived', async () => {
    const server = await startServer()
    expect((await chat(server, 'No marker at all')).status).toBe(409)
    expect((await chat(server, mockScenarioPrompt('missing', 'Unknown scenario.'))).status).toBe(409)

    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.unmatched).toMatchObject([
      { scenarioID: 'ambient', reason: 'the scenario is not registered', body: { messages: [{ content: 'No marker at all' }] } },
      { scenarioID: 'missing', reason: 'the scenario is not registered' },
    ])
  })

  it('holds a delayed step open for its full interval', async () => {
    const server = await startServer()
    await registerScenario(server, 'delayed', { steps: [{ text: 'Late answer', delayMs: 60 }] })

    const started = Date.now()
    expect(await responseText(await chat(server, mockScenarioPrompt('delayed', 'Wait.')))).toContain('Late answer')
    expect(Date.now() - started).toBeGreaterThanOrEqual(50)
  })

  it('abandons a held step when the client disconnects, and stays available', async () => {
    const server = await startServer()
    // Longer than this test can take. The abort ends it, not the timer.
    await registerScenario(server, 'interrupted', { steps: [{ text: 'Never sent', delayMs: 60_000 }] })

    const controller = new AbortController()
    const aborted = fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      signal: controller.signal,
      body: JSON.stringify({ stream: true, messages: [{ role: 'user', content: mockScenarioPrompt('interrupted', 'Cancel me.') }] }),
    })
    // The server records the step before it holds the response open, so this
    // waits for the hold itself rather than for an interval.
    await waitForStep(server, 'interrupted', 1)
    controller.abort()
    await expect(aborted).rejects.toThrow()

    // The step counts as consumed: the agent asked for it and the server chose it.
    expect(await readStatus(server, 'interrupted')).toMatchObject({ complete: true, nextStep: 1 })
    await registerScenario(server, 'after-abort', { steps: [{ text: 'Still serving' }] })
    expect(await responseText(await chat(server, mockScenarioPrompt('after-abort', 'Run.')))).toContain('Still serving')
  })

  it('refuses a malformed script at registration', async () => {
    const server = await startServer()
    const register = (script: unknown) => fetch(`${server.url}/__e2e/scenarios/invalid`, {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(script),
    })
    expect((await register({})).status).toBe(400)
    expect(await (await register({ steps: [{}] })).json()).toMatchObject({
      error: { message: expect.stringContaining('needs text, toolCalls, or error') },
    })
    expect(await (await register({ steps: [{ text: 'x', error: { status: 500, message: 'y' } }] })).json()).toMatchObject({
      error: { message: expect.stringContaining('cannot combine an error with output') },
    })
    expect(await (await register({ rules: [{ name: 'bad', when: { user: '(' }, respond: { text: 'x' } }] })).json()).toMatchObject({
      error: { message: expect.stringContaining('not a valid regular expression') },
    })
  })

  it('refuses every proxy tunnel, and records the host that it refused', async () => {
    // The mock is also the agents' proxy. A request that no setting of an agent
    // points at the mock then fails at once rather than leaving the machine.
    const server = await startServer()
    const { port } = new URL(server.url)
    const status = await new Promise<number | undefined>((resolve, reject) => {
      const tunnel = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'CONNECT', path: 'app.kiro.dev:443' })
      tunnel.on('connect', (response, socket) => {
        socket.destroy()
        resolve(response.statusCode)
      })
      tunnel.on('error', reject)
      tunnel.end()
    })
    expect(status).toBe(403)
    const log = await fetch(`${server.url}/__e2e/requests`).then(result => result.json())
    expect(log.http).toContainEqual({ method: 'CONNECT', path: 'app.kiro.dev:443', status: 403 })
  })

  // A client that sends a plain-HTTP request through the proxy sends it in absolute
  // form. Answering it by its path would serve a real host's request as a model
  // route, and the log would hide the host.
  it('refuses an absolute-form request to another host, and records that host', async () => {
    const server = await startServer()
    const { port } = new URL(server.url)
    const status = await new Promise<number | undefined>((resolve, reject) => {
      const proxied = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'GET', path: 'http://example.test/v1/models' })
      proxied.on('response', (response) => {
        response.resume()
        resolve(response.statusCode)
      })
      proxied.on('error', reject)
      proxied.end()
    })
    expect(status).toBe(403)
    const log = await fetch(`${server.url}/__e2e/requests`).then(result => result.json())
    expect(log.http).toContainEqual({ method: 'GET', path: 'http://example.test/v1/models', status: 403 })
    expect(server.refusedHosts()).toEqual(new Map([['example.test', 1]]))
  })

  // A client that ignores NO_PROXY for plain HTTP sends even its calls to the mock
  // in absolute form. The mock serves those as if they came directly.
  it('serves an absolute-form request to its own origin', async () => {
    const server = await startServer()
    const { port } = new URL(server.url)
    for (const host of ['127.0.0.1', 'localhost']) {
      const status = await new Promise<number | undefined>((resolve, reject) => {
        const proxied = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'GET', path: `http://${host}:${port}/healthz` })
        proxied.on('response', (response) => {
          response.resume()
          resolve(response.statusCode)
        })
        proxied.on('error', reject)
        proxied.end()
      })
      expect(status, host).toBe(200)
    }
    expect(server.refusedHosts().size).toBe(0)
  })

  // The origin is the host AND the port. A loopback address with another port is
  // another process on the machine, which the proxy must not reach for a client.
  it('refuses an absolute-form request to a loopback host on another port', async () => {
    const server = await startServer()
    const { port } = new URL(server.url)
    const otherPort = Number(port) === 1 ? 2 : 1
    const status = await new Promise<number | undefined>((resolve, reject) => {
      const proxied = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'GET', path: `http://127.0.0.1:${otherPort}/healthz` })
      proxied.on('response', (response) => {
        response.resume()
        resolve(response.statusCode)
      })
      proxied.on('error', reject)
      proxied.end()
    })
    expect(status).toBe(403)
    expect(server.refusedHosts()).toEqual(new Map([[`127.0.0.1:${otherPort}`, 1]]))
  })

  it('serves an absolute-form request to its own origin by its IPv6 loopback name', async () => {
    const server = await startServer()
    const { port } = new URL(server.url)
    const status = await new Promise<number | undefined>((resolve, reject) => {
      const proxied = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'GET', path: `http://[::1]:${port}/healthz` })
      proxied.on('response', (response) => {
        response.resume()
        resolve(response.statusCode)
      })
      proxied.on('error', reject)
      proxied.end()
    })
    expect(status).toBe(200)
    expect(server.refusedHosts().size).toBe(0)
  })

  it('hands out a copy of the refused hosts, which a caller cannot change', async () => {
    const server = await startServer()
    const copy = server.refusedHosts() as Map<string, number>
    copy.set('example.test', 9)
    expect(server.refusedHosts().size).toBe(0)
  })

  it('counts each refused host, for the report at the end of the run', async () => {
    const server = await startServer()
    const { port } = new URL(server.url)
    const connect = (target: string) => new Promise<void>((resolve, reject) => {
      const tunnel = httpRequest({ host: '127.0.0.1', port: Number(port), method: 'CONNECT', path: target })
      tunnel.on('connect', (_response, socket) => {
        socket.destroy()
        resolve()
      })
      tunnel.on('error', reject)
      tunnel.end()
    })
    await connect('app.kiro.dev:443')
    await connect('app.kiro.dev:443')
    await connect('github.com:443')
    expect(server.refusedHosts()).toEqual(new Map([['app.kiro.dev:443', 2], ['github.com:443', 1]]))
  })

  it('refuses a second registration under one identifier', async () => {
    const server = await startServer()
    await registerScenario(server, 'taken', { steps: [{ text: 'First' }] })
    const again = await fetch(`${server.url}/__e2e/scenarios/taken`, {
      method: 'PUT',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ steps: [{ text: 'Second' }] }),
    })
    expect(again.status).toBe(409)
  })

  it('refuses clean deletion when a scenario did not consume all steps', async () => {
    const server = await startServer()
    await registerScenario(server, 'unfinished', { steps: [{ text: 'First' }, { text: 'Second' }] })

    const refused = await fetch(`${server.url}/__e2e/scenarios/unfinished`, { method: 'DELETE' })
    expect(refused.status).toBe(409)
    expect(await refused.json()).toMatchObject({ complete: false, nextStep: 0, stepCount: 2 })

    const forced = await fetch(`${server.url}/__e2e/scenarios/unfinished?force=true`, { method: 'DELETE' })
    expect(forced.status).toBe(204)
    expect((await fetch(`${server.url}/__e2e/scenarios/unfinished`)).status).toBe(404)
  })
})

describe('createMockModelServer Amp service', () => {
  /** Open one socket of the thread actor, and collect what it receives. */
  async function actorSocket(server: MockModelServer, threadID: string) {
    const frames: unknown[] = []
    const socket = new WebSocket(`${server.url.replace('http:', 'ws:')}/actors/gateway/threadActor/websocket/?rvt-key=${threadID}`, ['rivet'])
    socket.addEventListener('message', event => frames.push(String(event.data) === 'pong' ? 'pong' : JSON.parse(String(event.data))))
    await new Promise<void>(resolve => socket.addEventListener('open', () => resolve(), { once: true }))
    await expect.poll(() => frames[0]).toBe('pong')
    return { socket, frames }
  }

  it('answers each inference of Amp\'s agent loop from the scenario its prompt marks', async () => {
    const server = await startServer()
    await registerScenario(server, 'amp-text', { steps: [{ text: 'Amp answer' }] })
    const created = await (await fetch(`${server.url}/api/thread-actors`, { method: 'POST', body: '{}' })).json() as { threadId: string }
    const { socket, frames } = await actorSocket(server, created.threadId)
    socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'client_append_user_msg', params: { content: [{ type: 'text', text: mockScenarioPrompt('amp-text', 'Say it.') }] } }))
    await expect.poll(() => frames.some(frame => JSON.stringify(frame).includes('Amp answer'))).toBe(true)
    const status = await readStatus(server, 'amp-text')
    expect(status).toMatchObject({ complete: true, nextStep: 1 })
    expect(status.requests[0]).toMatchObject({ protocol: 'anthropic-messages', stepIndex: 0 })
    socket.close()
  })

  it('records an inference whose scenario is not registered', async () => {
    const server = await startServer()
    const created = await (await fetch(`${server.url}/api/thread-actors`, { method: 'POST', body: '{}' })).json() as { threadId: string }
    const { socket, frames } = await actorSocket(server, created.threadId)
    socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'client_append_user_msg', params: { content: [{ type: 'text', text: mockScenarioPrompt('amp-missing', 'Say it.') }] } }))
    await expect.poll(() => frames.some(frame => JSON.stringify(frame).includes('error_set'))).toBe(true)
    const log = await (await fetch(`${server.url}/__e2e/requests`)).json() as { unmatched: { scenarioID: string }[] }
    expect(log.unmatched.map(entry => entry.scenarioID)).toContain('amp-missing')
    socket.close()
  })

  it('refuses an upgrade outside the actor gateway', async () => {
    const server = await startServer()
    const socket = new WebSocket(`${server.url.replace('http:', 'ws:')}/v1/responses`)
    await new Promise<void>(resolve => socket.addEventListener('error', () => resolve(), { once: true }))
  })
})
