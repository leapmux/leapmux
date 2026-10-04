import type { MockModelScenarioSpec, MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { afterEach, describe, expect, it } from 'vitest'
import { MOCK_MODEL_IDS, MODEL_KEY } from './mockAgentEnvironment'
import { mockScenarioPrompt } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'

const servers: MockModelServer[] = []
const modelPath = '/v1beta/models/gemini-2.5-pro'

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => server.close()))
})

async function scenario(id: string, script: Partial<MockModelScenarioSpec>) {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  servers.push(server)
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
    method: 'PUT',
    body: JSON.stringify({ steps: [], rules: [], ...script }),
  })
  expect(response.status).toBe(201)
  return server
}

async function status(server: MockModelServer, id: string): Promise<MockModelScenarioStatus> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`)
  expect(response.status).toBe(200)
  return await response.json() as MockModelScenarioStatus
}

function body(id: string) {
  return {
    systemInstruction: { parts: [{ text: 'NATIVE_SYSTEM_INSTRUCTION' }] },
    contents: [
      { role: 'user', parts: [{ text: mockScenarioPrompt(id, 'ACTUAL_USER_PROMPT') }] },
      { role: 'model', parts: [{ functionCall: { id: 'call-1', name: 'read_file', args: { path: 'WRONG_ARGUMENT_TEXT' } } }] },
      { role: 'user', parts: [{ functionResponse: { id: 'call-1', name: 'read_file', response: { output: 'WRONG_RESULT_TEXT' } } }] },
    ],
    tools: [{ functionDeclarations: [{ name: 'read_file', parametersJsonSchema: { type: 'object', properties: { path: { type: 'string' } } } }] }],
    generationConfig: { temperature: 0, thinkingConfig: { includeThoughts: true, thinkingBudget: 8192 } },
  }
}

function generate(server: MockModelServer, id: string, operation = 'streamGenerateContent', extra: RequestInit = {}) {
  return fetch(`${server.url}${modelPath}:${operation}${operation === 'streamGenerateContent' ? '?alt=sse' : ''}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-goog-api-key': MODEL_KEY },
    body: JSON.stringify(body(id)),
    ...extra,
  })
}

function streamRows(text: string): Record<string, unknown>[] {
  return text.split('\n\n').filter(Boolean).map((row) => {
    expect(row.startsWith('data: ')).toBe(true)
    return JSON.parse(row.slice(6)) as Record<string, unknown>
  })
}

describe('Google model API', () => {
  it('matches native instructions and the user prompt without tool results and records the unchanged request', async () => {
    const id = 'google-native-request'
    const server = await scenario(id, { rules: [{
      name: 'native-context',
      once: true,
      when: { protocol: 'google-generative-language', system: '^NATIVE_SYSTEM_INSTRUCTION$', user: `^ACTUAL_USER_PROMPT\\n\\nLEAPMUXE2ESCENARIO:${id}$` },
      respond: { text: 'Accepted the native request.' },
    }] })
    const response = await generate(server, id, 'generateContent')
    expect(response.status).toBe(200)
    expect(response.headers.get('content-type')).toContain('application/json')
    await response.json()
    const receipt = await status(server, id)
    expect(receipt.ruleMatches).toEqual({ 'native-context': 1 })
    expect(receipt.unexpectedRequests).toEqual([])
    expect(receipt.requests[0]).toMatchObject({ protocol: 'google-generative-language', path: `${modelPath}:generateContent`, body: body(id), mockCredential: { kind: 'api-key', accepted: true } })
    expect(receipt.requests[0]?.response?.status).toBe(200)
    expect(JSON.stringify(receipt)).not.toContain(MODEL_KEY)
  })

  it('streams Unicode reasoning and text before native tool calls and preserves zero usage', async () => {
    const id = 'google-native-stream'
    const server = await scenario(id, { steps: [{
      reasoning: '생각 🧪',
      text: 'Answer 🧪',
      stream: { chunkChars: 2, delayMs: 0 },
      toolCalls: [{ id: 'native-call-42', name: 'read_file', arguments: { zero: 0, disabled: false, empty: '' } }],
      usage: { inputTokens: 0, outputTokens: 0 },
    }] })
    const response = await generate(server, id)
    expect(response.status).toBe(200)
    expect(response.headers.get('content-type')).toContain('text/event-stream')
    const rows = streamRows(await response.text())
    const parts = rows.flatMap(row => (row.candidates as { content: { parts: Record<string, unknown>[] } }[]).flatMap(candidate => candidate.content.parts))
    expect(parts.filter(part => part.thought === true).map(part => part.text).join('')).toBe('생각 🧪')
    expect(parts.filter(part => part.text !== undefined && part.thought !== true).map(part => part.text).join('')).toBe('Answer 🧪')
    expect(parts.at(-1)).toEqual({ functionCall: { id: 'native-call-42', name: 'read_file', args: { zero: 0, disabled: false, empty: '' } } })
    expect(rows.at(-1)).toMatchObject({ candidates: [{ finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 0, candidatesTokenCount: 0, totalTokenCount: 0 } })
    expect((await status(server, id)).nextStep).toBe(1)
  })

  it('counts tokens without consuming a model turn and handles an empty generation', async () => {
    const id = 'google-auxiliary-count'
    const server = await scenario(id, { steps: [{ text: '', usage: { inputTokens: 0, outputTokens: 0 } }] })
    const count = await generate(server, id, 'countTokens')
    expect(count.status).toBe(200)
    expect(await count.json()).toEqual({ totalTokens: expect.any(Number) })
    expect((await status(server, id)).requests).toEqual([])
    expect((await status(server, id)).nextStep).toBe(0)
    const response = await generate(server, id, 'generateContent')
    expect(response.status).toBe(200)
    expect(await response.json()).toMatchObject({ candidates: [{ content: { role: 'model', parts: [{ text: '' }] }, finishReason: 'STOP' }] })
    expect((await status(server, id)).nextStep).toBe(1)
  })

  it('counts empty contents as zero without recording a model request', async () => {
    const id = 'google-empty-count'
    const server = await scenario(id, { steps: [{ text: 'Must remain queued.' }] })
    const response = await generate(server, id, 'countTokens', { body: JSON.stringify({ contents: [] }) })
    expect(response.status).toBe(200)
    expect(await response.json()).toEqual({ totalTokens: 0 })
    expect((await status(server, id)).requests).toEqual([])
  })

  it.each(['', '{broken', 'null', '[]', '{}', '{"contents":null}'])('rejects malformed request bytes before scenario consumption: %j', async (body) => {
    const id = 'google-invalid-request'
    const server = await scenario(id, { steps: [{ text: 'Must remain queued.' }] })
    const response = await generate(server, id, 'generateContent', { body })
    expect(response.status).toBe(400)
    expect(await response.json()).toMatchObject({ error: { code: 400, status: 'INVALID_ARGUMENT' } })
    expect((await status(server, id)).requests).toEqual([])
  })

  it('rejects a non-POST method without selecting an answer', async () => {
    const id = 'google-invalid-method'
    const server = await scenario(id, { steps: [{ text: 'Must remain queued.' }] })
    const response = await fetch(`${server.url}${modelPath}:generateContent`, { headers: { 'x-goog-api-key': MODEL_KEY } })
    expect(response.status).toBe(405)
    expect((await status(server, id)).requests).toEqual([])
  })

  it.each([
    { label: 'absent', headers: {}, query: '' },
    { label: 'unknown header', headers: { 'x-goog-api-key': 'private-never-log' }, query: '' },
    { label: 'unknown URL key', headers: {}, query: '?key=private-never-log' },
    { label: 'conflicting credentials', headers: { 'x-goog-api-key': MODEL_KEY }, query: '?key=private-never-log' },
  ])('rejects $label credentials before it consumes a step and records no secret', async ({ headers, query }) => {
    const id = 'google-credential-rejection'
    const server = await scenario(id, { steps: [{ text: 'Must remain queued.' }] })
    const response = await fetch(`${server.url}${modelPath}:generateContent${query}`, { method: 'POST', headers, body: JSON.stringify(body(id)) })
    expect(response.status).toBe(401)
    expect((await status(server, id)).requests).toEqual([])
    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.text())
    expect(log).not.toContain('private-never-log')
    expect(log).not.toContain(MODEL_KEY)
  })

  it('accepts the exact fixture URL key without retaining its query', async () => {
    const id = 'google-url-key'
    const server = await scenario(id, { steps: [{ text: 'URL credential accepted.' }] })
    const response = await fetch(`${server.url}${modelPath}:generateContent?key=${MODEL_KEY}`, { method: 'POST', body: JSON.stringify(body(id)) })
    expect(response.status).toBe(200)
    await response.text()
    expect((await status(server, id)).requests[0]?.mockCredential).toEqual({ kind: 'api-key', accepted: true })
    expect(await fetch(`${server.url}/__e2e/requests`).then(response => response.text())).not.toContain(MODEL_KEY)
  })

  it('records an unscripted model request without generating an answer', async () => {
    const id = 'google-unscripted'
    const server = await scenario(id, { rules: [{ name: 'nonmatching', when: { user: '^NEVER_MATCH_THIS_PROMPT$' }, respond: { text: 'Must never answer.' } }] })
    const response = await generate(server, id)
    expect(response.status).toBe(409)
    const receipt = await status(server, id)
    expect(receipt.unexpectedRequests).toHaveLength(1)
    expect(receipt.unexpectedRequests[0]?.body).toEqual(body(id))
  })

  it('delivers the native error envelope and stores its status', async () => {
    const id = 'google-native-error'
    const server = await scenario(id, { steps: [{ error: { status: 429, code: 'RESOURCE_EXHAUSTED', message: 'The test quota is exhausted.' } }] })
    const response = await generate(server, id)
    expect(response.status).toBe(429)
    expect(await response.json()).toEqual({ error: { code: 429, status: 'RESOURCE_EXHAUSTED', message: 'The test quota is exhausted.' } })
    expect((await status(server, id)).requests[0]?.response).toMatchObject({ status: 429, serviceError: { code: 'RESOURCE_EXHAUSTED' } })
  })

  it('releases a held stream when the native client cancels and stops generation', async () => {
    const id = 'google-native-cancellation'
    const server = await scenario(id, { steps: [{ text: 'FIRST_SECOND', stream: { chunkChars: 6, delayMs: 0, gates: [{ afterChunk: 1, name: 'first-piece' }] } }] })
    const controller = new AbortController()
    const response = await generate(server, id, 'streamGenerateContent', { signal: controller.signal })
    expect(response.status).toBe(200)
    const reader = response.body!.getReader()
    const first = await reader.read()
    expect(new TextDecoder().decode(first.value)).toContain('FIRST_')
    await expect.poll(async () => (await status(server, id)).pendingGates).toEqual(['first-piece'])
    controller.abort()
    await reader.cancel().catch((error: unknown) => expect(error).toMatchObject({ name: 'AbortError' }))
    await expect.poll(async () => (await status(server, id)).pendingGates).toEqual([])
    expect((await status(server, id)).nextStep).toBe(1)
  })
})
