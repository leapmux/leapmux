import type { MockModelScript } from './mockModelScenario'
import type { MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { afterEach, describe, expect, it } from 'vitest'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
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

describe('createMockModelServer', () => {
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

  it('advertises the pinned model identifiers and the client identity routes', async () => {
    const server = await startServer()
    const models = await fetch(`${server.url}/models`).then(response => response.json())
    expect(models.data.map((model: { id: string }) => model.id)).toEqual([...MOCK_MODEL_IDS])
    expect(models.data[0]).toMatchObject({
      capabilities: { supports: { vision: true }, limits: { max_context_window_tokens: 128000 } },
    })

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
