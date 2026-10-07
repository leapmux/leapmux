import type { MockModelScenarioInput } from './mockModelScenario'
import type { MockModelCredential, MockModelScenarioStatus } from './mockModelScript'
import type { MockModelServer } from './mockModelServer'
import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { request as httpRequest } from 'node:http'
import { connect as connectHttp2 } from 'node:http2'
import { connect as connectTcp } from 'node:net'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { isObject } from '../../../src/lib/jsonPick'
import { ANCESTOR_INSTRUCTION_SENTINEL } from './ancestorInstructions'
import { encodeLengthDelimited, encodeStringField } from './cursorProtobuf'
import { CURSOR_RUN_PATH, CURSOR_TASK_TOOL } from './cursorSurface'
import { cursorRequestContextReply } from './cursorTestFrames'
import { connectFrame, takeConnectFrames } from './cursorWire'
import { MOCK_COPILOT_GITHUB_TOKEN, MOCK_MODEL_IDS, MOCK_MODELS, MODEL_KEY } from './mockAgentEnvironment'
import { mockScenarioPrompt, readScenarioStatus } from './mockModelScenario'
import { AMBIENT_SCENARIO_ID } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { CLAUDE_SUBAGENT_HANDBACK_TOOL, claudeSubagentHandbackToolDefinition } from './providerToolCalls'

const servers: MockModelServer[] = []
const credentialCases: { label: string, headers: Record<string, string>, credential: MockModelCredential }[] = [
  { label: 'mock bearer', headers: { authorization: `Bearer ${MODEL_KEY}` }, credential: { kind: 'bearer', accepted: true } },
  { label: 'mock API key', headers: { 'x-api-key': MODEL_KEY }, credential: { kind: 'api-key', accepted: true } },
  { label: 'wrong bearer', headers: { authorization: 'Bearer private-credential-never-log' }, credential: { kind: 'bearer', accepted: false } },
  { label: 'mixed credentials', headers: { 'authorization': 'Bearer private-credential-never-log', 'x-api-key': MODEL_KEY }, credential: { kind: 'api-key', accepted: false } },
  { label: 'absent credential', headers: {}, credential: { kind: 'none', accepted: false } },
]

afterEach(async () => {
  await Promise.all(servers.splice(0).map(server => server.close()))
})

async function startServer(): Promise<MockModelServer> {
  const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
  servers.push(server)
  return server
}

async function registerScenario(server: MockModelServer, id: string, script: MockModelScenarioInput): Promise<void> {
  await registerRawScenario(server, id, script)
}

async function registerRawScenario(server: MockModelServer, id: string, script: unknown): Promise<void> {
  const response = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(script),
  })
  expect(response.status).toBe(201)
}

/** Wait until the scenario consumed `count` steps, so no test sizes an interval. */
async function waitForStep(server: MockModelServer, id: string, count: number): Promise<void> {
  const deadline = Date.now() + 5_000
  while (Date.now() < deadline) {
    if ((await readScenarioStatus(server.url, id)).nextStep >= count)
      return
    await new Promise(resolve => setTimeout(resolve, 5))
  }
  throw new Error(`Model scenario ${id} did not reach step ${count}`)
}

/** Wait for a response gate to hold a real model request. */
async function waitForGate(server: MockModelServer, id: string, gate: string): Promise<MockModelScenarioStatus> {
  const deadline = Date.now() + 4_000
  while (Date.now() < deadline) {
    const status = await readScenarioStatus(server.url, id)
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

/** Send the native Cursor Run frame with the conversation that owns the prompt, and read the whole answer. */
async function cursorRun(server: MockModelServer, conversationID: string, prompt: string, signal?: AbortSignal, rules: readonly { path: string, content: string }[] = []): Promise<void> {
  const response = await cursorResponse(server, conversationID, prompt, signal, rules)
  expect(response.status).toBe(200)
  await response.arrayBuffer()
}

/**
 * Open a native Cursor turn, and answer the request context query that the surface sends at the start of the turn
 * with `rules`. The surface numbers that query 301.
 */
function cursorResponse(server: MockModelServer, conversationID: string, prompt: string, signal?: AbortSignal, rules: readonly { path: string, content: string }[] = []): Promise<Response> {
  const action = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, encodeStringField(1, prompt))))
  const frame = encodeLengthDelimited(1, Buffer.concat([Buffer.from(action), Buffer.from(encodeStringField(5, conversationID))]))
  return fetch(`${server.url}${CURSOR_RUN_PATH}`, {
    method: 'POST',
    headers: { 'content-type': 'application/connect+proto' },
    body: Buffer.concat([Buffer.from(connectFrame(frame)), Buffer.from(connectFrame(cursorRequestContextReply(301, rules)))]),
    ...(signal ? { signal } : {}),
  })
}

describe('createMockModelServer', () => {
  it('selects the native user prompt instead of a later truncated session title', async () => {
    const server = await startServer()
    const scenario = 'native-deepseek-compaction'
    await registerScenario(server, scenario, { steps: [{ text: 'The native prompt selected the intended scenario.' }] })
    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'x-api-key': MODEL_KEY },
      body: JSON.stringify({
        model: MOCK_MODELS.anthropic,
        stream: false,
        messages: [{ role: 'user', content: mockScenarioPrompt(scenario, 'Compact the actual native context.') }],
        dsh_session_log: { events: [{ type: 'session/title', data: { title: mockScenarioPrompt('native-deepseek-compa', 'Native truncated title.') } }] },
      }),
    })
    expect(response.status).toBe(200)
    expect(await response.json()).toMatchObject({ content: [{ type: 'text', text: 'The native prompt selected the intended scenario.' }] })
    expect(await readScenarioStatus(server.url, scenario)).toMatchObject({ complete: true, nextStep: 1, unexpectedRequests: [] })
  })

  for (const stream of [true, false]) {
    it(`ends the delivered native Claude child continuation without a second handback or content step (${stream ? 'SSE' : 'JSON'})`, async () => {
      const server = await startServer()
      const scenarioId = `native-claude-delivered-${stream ? 'sse' : 'json'}`
      const report = '  The exact delivered child report.\nKeep Unicode 실제 내용 🧪 and whitespace.  '
      await registerScenario(server, scenarioId, { steps: [], fallback: { text: 'The content fallback must remain unused.' } })
      const response = await fetch(`${server.url}/v1/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          model: MOCK_MODELS.anthropic,
          stream,
          tools: [claudeSubagentHandbackToolDefinition()],
          messages: [
            { role: 'user', content: mockScenarioPrompt(scenarioId, 'Deliver the exact original child report.') },
            { role: 'assistant', content: [{ type: 'text', text: report }, { type: 'tool_use', id: 'native-delivered-handback', name: CLAUDE_SUBAGENT_HANDBACK_TOOL, input: { message: report } }] },
            { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-delivered-handback', content: [{ type: 'text', text: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) }] }] },
            { role: 'system', content: [{ type: 'text', text: 'The native agent catalog remains available.' }] },
          ],
        }),
      })
      expect(response.status).toBe(200)
      if (stream) {
        const events: unknown[] = (await response.text()).split('\n').filter(line => line.startsWith('data: ')).map(line => JSON.parse(line.slice(6)))
        expect(events.flatMap(event => isObject(event) && isObject(event.delta) && event.delta.type === 'text_delta' && typeof event.delta.text === 'string' ? [event.delta.text] : []).join('')).toBe(report)
        expect(events.some(event => isObject(event) && isObject(event.content_block) && event.content_block.type === 'tool_use')).toBe(false)
        expect(events.some(event => isObject(event) && isObject(event.delta) && event.delta.stop_reason === 'end_turn')).toBe(true)
      }
      else {
        expect(await response.json()).toMatchObject({ content: [{ type: 'text', text: report }], stop_reason: 'end_turn' })
      }
      const status = await readScenarioStatus(server.url, scenarioId)
      expect(status.nextStep).toBe(0)
      expect(status.unexpectedRequests).toEqual([])
      expect(status.requests).toHaveLength(1)
      expect(status.requests[0]?.rule).toBe('claude-child-handback-complete')
    })
  }

  it('delivers the exact scripted Claude child report through its offered native tool in an actual SSE response', async () => {
    const server = await startServer()
    const report = '  Original child report.\nSecond line: 실제 내용 🧪\t  '
    await registerScenario(server, 'native-claude-handback-sse', { steps: [{ text: report }] })
    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model: MOCK_MODELS.anthropic, stream: true, tools: [claudeSubagentHandbackToolDefinition()], messages: [{ role: 'user', content: mockScenarioPrompt('native-claude-handback-sse', 'Finish the assigned child report.') }] }),
    })
    const events: unknown[] = (await responseText(response)).split('\n').filter(line => line.startsWith('data: ')).map(line => JSON.parse(line.slice(6)))
    const text = events.flatMap(event => isObject(event) && isObject(event.delta) && event.delta.type === 'text_delta' && typeof event.delta.text === 'string' ? [event.delta.text] : []).join('')
    expect(text).toBe(report)
    const start = events.find(event => isObject(event) && isObject(event.content_block) && event.content_block.type === 'tool_use' && event.content_block.name === CLAUDE_SUBAGENT_HANDBACK_TOOL)
    expect(start).toBeDefined()
    if (!isObject(start) || typeof start.index !== 'number')
      throw new Error('The Claude child response omitted its native report delivery tool.')
    const input = events.flatMap(event => isObject(event) && event.index === start.index && isObject(event.delta) && event.delta.type === 'input_json_delta' && typeof event.delta.partial_json === 'string' ? [event.delta.partial_json] : []).join('')
    expect(JSON.parse(input)).toEqual({ message: report })
    expect(events.some(event => isObject(event) && isObject(event.delta) && event.delta.stop_reason === 'tool_use')).toBe(true)
  })

  it('delivers the exact scripted Claude child report in an actual nonstreaming Anthropic response', async () => {
    const server = await startServer()
    const report = 'The original nonstreaming child report.\nKeep every word.'
    await registerScenario(server, 'native-claude-handback-json', { steps: [{ text: report }] })
    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ model: MOCK_MODELS.anthropic, stream: false, tools: [claudeSubagentHandbackToolDefinition()], messages: [{ role: 'user', content: mockScenarioPrompt('native-claude-handback-json', 'Finish the assigned child report.') }] }),
    })
    expect(response.status).toBe(200)
    const body: unknown = await response.json()
    expect(body).toMatchObject({ content: expect.arrayContaining([
      { type: 'text', text: report },
      expect.objectContaining({ type: 'tool_use', name: CLAUDE_SUBAGENT_HANDBACK_TOOL, input: { message: report } }),
    ]), stop_reason: 'tool_use' })
  })

  for (const preface of ['', 'P']) {
    it(`closes an accepted socket with the undecided preface ${JSON.stringify(preface)}`, async () => {
      const server = await createMockModelServer({ models: ['native-close-unit'] })
      const socket = connectTcp(Number(new URL(server.url).port), '127.0.0.1')
      await new Promise<void>((resolve, reject) => {
        socket.once('connect', resolve)
        socket.once('error', reject)
      })
      if (preface)
        socket.write(preface)
      const closing = server.close()
      try {
        await expect.poll(() => socket.destroyed).toBe(true)
        await closing
      }
      finally {
        socket.destroy()
        await closing
      }
    })
  }

  it('closes a retained native HTTP2 session after its response completes', async () => {
    const server = await createMockModelServer({ models: ['native-http2-close-unit'] })
    const client = connectHttp2(server.url)
    try {
      const stream = client.request({ ':path': '/healthz' })
      stream.resume()
      await new Promise<void>((resolve, reject) => {
        stream.once('end', resolve)
        stream.once('error', reject)
        stream.end()
      })
      const closing = server.close()
      try {
        await expect.poll(() => client.destroyed || client.closed).toBe(true)
        await closing
      }
      finally {
        client.destroy()
        await closing
      }
    }
    finally {
      client.destroy()
    }
  })

  for (const path of ['/v1/chat/completions', '/v1/responses', '/v1/messages']) {
    it(`holds internally generated output before the complete JSON response on ${path}`, async () => {
      const server = await startServer()
      await registerScenario(server, 'buffered-json-generation', { steps: [{ reasoning: 'THINK', text: 'DONE', stream: { chunkChars: 2, delayMs: 0, gates: [{ afterChunk: 1, name: 'buffered-thinking' }] } }] })
      let delivered = false
      const prompt = mockScenarioPrompt('buffered-json-generation', 'Generate a complete JSON answer.')
      const pending = fetch(`${server.url}${path}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: prompt }], input: [{ role: 'user', content: prompt }] }),
      }).then(async (response) => {
        delivered = true
        expect(response.status).toBe(200)
        return response.text()
      })
      await waitForStep(server, 'buffered-json-generation', 1)
      const status = await readScenarioStatus(server.url, 'buffered-json-generation')
      expect(status.pendingGates).toContain('buffered-thinking')
      expect(delivered).toBe(false)
      expect(status.requests[0]?.response).toBeUndefined()
      expect((await fetch(`${server.url}/__e2e/scenarios/buffered-json-generation/gates/buffered-thinking/release`, { method: 'POST' })).status).toBe(204)
      expect(await pending).toContain('DONE')
    })
  }

  it('atomically releases only a held native response without relaxing strict release', async () => {
    const server = await startServer()
    await registerScenario(server, 'atomic-gate-cleanup', { steps: [{ text: 'The held answer ended.', gate: 'native-hold' }] })
    const cleanup = (gate: string) => fetch(`${server.url}/__e2e/scenarios/atomic-gate-cleanup/gates/${gate}/release-if-held`, { method: 'POST' })
    const missing = await cleanup('not-reached')
    expect(missing.status).toBe(200)
    expect(await missing.json()).toEqual({ released: false })
    const pending = chat(server, mockScenarioPrompt('atomic-gate-cleanup', 'Hold this turn.')).then(response => response.text())
    await waitForGate(server, 'atomic-gate-cleanup', 'native-hold')
    const held = await cleanup('native-hold')
    expect(held.status).toBe(200)
    expect(await held.json()).toEqual({ released: true })
    expect(await pending).toContain('The held answer ended.')
    const repeated = await cleanup('native-hold')
    expect(repeated.status).toBe(200)
    expect(await repeated.json()).toEqual({ released: false })
    expect((await fetch(`${server.url}/__e2e/scenarios/atomic-gate-cleanup/gates/native-hold/release`, { method: 'POST' })).status).toBe(409)
  })

  it('atomically accepts cleanup after a real native HTTP response was cancelled', async () => {
    const server = await startServer()
    await registerScenario(server, 'atomic-cancelled-cleanup', { steps: [{ text: 'Do not complete this turn.', gate: 'native-cancelled' }] })
    const controller = new AbortController()
    const response = fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ stream: true, messages: [{ role: 'user', content: mockScenarioPrompt('atomic-cancelled-cleanup', 'Cancel this turn.') }] }),
    }).catch(error => error)
    await waitForGate(server, 'atomic-cancelled-cleanup', 'native-cancelled')
    controller.abort()
    await response
    await expect.poll(async () => (await readScenarioStatus(server.url, 'atomic-cancelled-cleanup')).pendingGates).toEqual([])
    const cleanup = await fetch(`${server.url}/__e2e/scenarios/atomic-cancelled-cleanup/gates/native-cancelled/release-if-held`, { method: 'POST' })
    expect(cleanup.status).toBe(200)
    expect(await cleanup.json()).toEqual({ released: false })
    expect((await readScenarioStatus(server.url, 'atomic-cancelled-cleanup')).requests[0]?.response).toBeUndefined()
  })

  for (const path of ['/v1/chat/completions', '/v1/responses', '/v1/messages']) {
    for (const metadata of [{ completionGate: 'child-finish' }, { taskProgress: 'A child-only native update.' }, { nativeExecution: { modelId: 'default' } }]) {
      it(`rejects provider-service metadata on the generic model route ${path} with ${Object.keys(metadata)[0]}`, async () => {
        const server = await startServer()
        await registerRawScenario(server, 'service-only-tool', { steps: [{ toolCalls: [{ id: 'call-1', name: 'Task', arguments: { prompt: 'Read.' }, ...metadata }] }] })
        const response = await fetch(`${server.url}${path}`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: mockScenarioPrompt('service-only-tool', 'Use this tool.') }], input: [{ role: 'user', content: mockScenarioPrompt('service-only-tool', 'Use this tool.') }] }),
        })
        expect(response.status).toBe(400)
        expect(await response.text()).toContain('metadata for provider service tools')
        const status = await readScenarioStatus(server.url, 'service-only-tool')
        expect(status.unexpectedRequests).toHaveLength(1)
        expect(status.complete).toBe(false)
      })
    }
  }

  it('records the actual HTTP response status and headers after an error reaches the client', async () => {
    const server = await startServer()
    await registerScenario(server, 'error-response-receipt', { steps: [{ error: { status: 429, code: 'rate_limit_exceeded', message: 'The quota ended.' } }] })
    const response = await chat(server, mockScenarioPrompt('error-response-receipt', 'Report this error.'), false)
    expect(response.status).toBe(429)
    expect(await response.json()).toMatchObject({ error: { code: 'rate_limit_exceeded', message: 'The quota ended.' } })
    expect((await readScenarioStatus(server.url, 'error-response-receipt')).requests[0]?.response)
      .toMatchObject({ status: 429, headers: { 'content-type': 'application/json' }, serviceError: { code: 'rate_limit_exceeded', message: 'The quota ended.' } })
  })

  it('fails a chat completion stream after one partial delta when the error is mid-stream', async () => {
    const server = await startServer()
    await registerScenario(server, 'mid-stream-error', { steps: [{ error: { status: 500, code: 'stream_error', message: 'The stream broke.', midStream: true } }] })
    const response = await chat(server, mockScenarioPrompt('mid-stream-error', 'Report this error.'))
    expect(response.status).toBe(200)
    expect(response.headers.get('content-type')).toContain('text/event-stream')
    const events = (await response.text()).split('\n\n').filter(Boolean).map(event => JSON.parse(event.replace(/^data: /, '')))
    expect(events).toEqual([
      expect.objectContaining({ choices: [{ index: 0, delta: { role: 'assistant', content: 'partial ' }, finish_reason: null }] }),
      { error: { message: 'The stream broke.', code: 'stream_error' } },
    ])
    expect((await readScenarioStatus(server.url, 'mid-stream-error')).requests[0]?.response)
      .toMatchObject({ status: 200, serviceError: { code: 'stream_error', message: 'The stream broke.' } })
  })

  it('refuses a mid-stream error to a request that asks for no stream', async () => {
    const server = await startServer()
    await registerScenario(server, 'mid-stream-unstreamed', { steps: [{ error: { status: 500, message: 'The stream broke.', midStream: true } }] })
    const response = await chat(server, mockScenarioPrompt('mid-stream-unstreamed', 'Report this error.'), false)
    expect(response.status).toBe(400)
    expect(await response.text()).toContain('only to a request that asks for a stream')
  })

  for (const path of ['/v1/responses', '/v1/messages']) {
    it(`refuses a mid-stream error on ${path}, which has no such shape`, async () => {
      const server = await startServer()
      await registerScenario(server, 'mid-stream-elsewhere', { steps: [{ error: { status: 500, message: 'The stream broke.', midStream: true } }] })
      const content = mockScenarioPrompt('mid-stream-elsewhere', 'Report this error.')
      const response = await fetch(`${server.url}${path}`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'mock-model', stream: true, messages: [{ role: 'user', content }], input: [{ role: 'user', content }] }),
      })
      expect(response.status).toBe(400)
      expect(await response.text()).toContain('mid-stream model error on the OpenAI Chat Completions route only')
    })
  }

  it('leaves an interrupted HTTP response without a successful delivery receipt', async () => {
    const server = await startServer()
    await registerScenario(server, 'cancel-response-receipt', { steps: [{ text: 'Do not deliver this answer.', gate: 'cancel-response' }] })
    const controller = new AbortController()
    const response = fetch(`${server.url}/v1/chat/completions`, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ messages: [{ role: 'user', content: mockScenarioPrompt('cancel-response-receipt', 'Hold this answer.') }] }),
    }).catch(error => error)
    await waitForGate(server, 'cancel-response-receipt', 'cancel-response')
    controller.abort()
    await response
    expect((await readScenarioStatus(server.url, 'cancel-response-receipt')).requests[0]?.response).toBeUndefined()
  })

  it('writes the native Copilot quota snapshot header with an explicit mock entitlement', async () => {
    const server = await startServer()
    const resetsAt = 1_791_072_000
    await registerScenario(server, 'copilot-native-quota', { steps: [{ text: 'The quota snapshot arrived.', rateLimits: { type: 'premium_interactions', status: 'allowed', utilization: 0.73, resetsAt } }] })
    const response = await chat(server, mockScenarioPrompt('copilot-native-quota', 'Report this turn.'), false)
    expect(response.headers.get('x-quota-snapshot-premium_interactions')).toBe(`ent=100&rem=27&ov=0&ovPerm=false&rst=${new Date(resetsAt * 1000).toISOString()}`)
    await response.text()
  })

  for (const { label, headers, credential } of credentialCases) {
    it(`records a redacted credential receipt for ${label}`, async () => {
      const server = await startServer()
      await registerScenario(server, 'credential-receipt', { steps: [{ text: 'A model answer.' }] })
      const response = await fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', ...headers },
        body: JSON.stringify({ stream: false, messages: [{ role: 'user', content: mockScenarioPrompt('credential-receipt', 'Answer once.') }] }),
      })
      expect(response.status).toBe(200)
      await response.text()
      const status = await readScenarioStatus(server.url, 'credential-receipt')
      expect(status.requests[0]).toHaveProperty('mockCredential', credential)
      expect(JSON.stringify(status)).not.toContain('private-credential-never-log')
    })
  }

  it('holds native Cursor output after a chunk and excludes completed text before release', async () => {
    const server = await startServer()
    await registerRawScenario(server, 'cursor-stream-gate', { steps: [{ reasoning: 'THINKING', text: 'ANSWER', stream: { chunkChars: 4, delayMs: 0, gates: [{ afterChunk: 1, name: 'cursor-partial' }] } }] })
    const response = await cursorResponse(server, 'cursor-stream-id', mockScenarioPrompt('cursor-stream-gate', 'Stream the native answer.'))
    const reader = response.body!.getReader()
    const first = await reader.read()
    const prefix = Buffer.from(first.value!).toString('utf8')
    expect(prefix).toContain('THIN')
    expect(prefix).not.toContain('ANSWER')
    await waitForGate(server, 'cursor-stream-gate', 'cursor-partial')
    expect((await fetch(`${server.url}/__e2e/scenarios/cursor-stream-gate/gates/cursor-partial/release`, { method: 'POST' })).status).toBe(204)
    let remaining = ''
    for (;;) {
      const next = await reader.read()
      if (next.done)
        break
      remaining += Buffer.from(next.value).toString('utf8')
    }
    expect(remaining).toContain('KING')
    expect(remaining).toContain('ANSWER')
  })

  it('sends a real nested Cursor task delta before holding its completion', async () => {
    const server = await startServer()
    await registerRawScenario(server, 'cursor-child-progress', { steps: [{ toolCalls: [{ id: 'native-child', name: CURSOR_TASK_TOOL, arguments: { description: 'Read the child file', prompt: 'Read the file.', report: 'FINISHED_CHILD_REPORT' }, taskProgress: 'LIVE_CHILD_NATIVE_TEXT', completionGate: 'child-completion' }], text: 'ROOT_FINISHED_TEXT' }] })
    const response = await cursorResponse(server, 'cursor-child-progress-id', mockScenarioPrompt('cursor-child-progress', 'Start the native child.'))
    const reader = response.body!.getReader()
    let prefix = ''
    while (!prefix.includes('LIVE_CHILD_NATIVE_TEXT')) {
      const part = await reader.read()
      expect(part.done).toBe(false)
      prefix += Buffer.from(part.value!).toString('utf8')
    }
    await waitForGate(server, 'cursor-child-progress', 'child-completion')
    expect(prefix).not.toContain('FINISHED_CHILD_REPORT')
    expect(prefix).not.toContain('ROOT_FINISHED_TEXT')
    const released = await fetch(`${server.url}/__e2e/scenarios/cursor-child-progress/gates/child-completion/release`, { method: 'POST' })
    expect(released.status).toBe(204)
    let remaining = ''
    for (;;) {
      const part = await reader.read()
      if (part.done)
        break
      remaining += Buffer.from(part.value!).toString('utf8')
    }
    expect(remaining).toContain('FINISHED_CHILD_REPORT')
    expect(remaining).toContain('ROOT_FINISHED_TEXT')
  })

  it('records the actual streamed quota headers only after the response completes', async () => {
    const server = await startServer()
    await registerScenario(server, 'streamed-response-headers', { steps: [{ text: 'A complete native answer.', rateLimits: { type: 'five_hour', status: 'allowed', utilization: 0 }, stream: { chunkChars: 4, delayMs: 0, gates: [{ afterChunk: 1, name: 'delivered-chunk' }] } }] })
    const response = await chat(server, mockScenarioPrompt('streamed-response-headers', 'Stream this answer.'))
    const reader = response.body!.getReader()
    await reader.read()
    await waitForGate(server, 'streamed-response-headers', 'delivered-chunk')
    expect((await readScenarioStatus(server.url, 'streamed-response-headers')).requests[0]?.response).toBeUndefined()
    expect((await fetch(`${server.url}/__e2e/scenarios/streamed-response-headers/gates/delivered-chunk/release`, { method: 'POST' })).status).toBe(204)
    for (;;) {
      if ((await reader.read()).done)
        break
    }
    const receipt = (await readScenarioStatus(server.url, 'streamed-response-headers')).requests[0]?.response
    expect(receipt?.headers['content-type']).toBe(response.headers.get('content-type'))
    expect(receipt?.headers['anthropic-ratelimit-unified-5h-utilization']).toBe(response.headers.get('anthropic-ratelimit-unified-5h-utilization'))
    expect(receipt?.headers['anthropic-ratelimit-unified-5h-utilization']).toBe('0')
  })

  it('records an unexpected request when a capture makes its stream gate unreachable', async () => {
    const server = await startServer()
    await registerRawScenario(server, 'captured-unreachable-gate', { steps: [{ text: '{{reply}}', captures: { reply: 'CAPTURE: (x)' }, stream: { chunkChars: 1, delayMs: 0, gates: [{ afterChunk: 2, name: 'unreachable' }] } }] })
    const response = await chat(server, mockScenarioPrompt('captured-unreachable-gate', 'CAPTURE: x'), false)
    expect(response.ok).toBe(false)
    const status = await readScenarioStatus(server.url, 'captured-unreachable-gate')
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toEqual([expect.objectContaining({ reason: expect.stringContaining('exceeds the emitted chunk count') })])
  })

  for (const stream of [false, true]) {
    it(`ends an empty tool-call list as a normal chat turn with stream ${stream}`, async () => {
      const server = await startServer()
      await registerScenario(server, 'empty-chat-tools', { steps: [{ text: '', toolCalls: [] }] })
      const response = await chat(server, mockScenarioPrompt('empty-chat-tools', 'Return no tool call.'), stream)
      if (stream) {
        const body = await responseText(response)
        expect(body).not.toContain('"tool_calls"')
        expect(body).toContain('"finish_reason":"stop"')
      }
      else {
        expect(await response.json()).toMatchObject({ choices: [{ message: { role: 'assistant', content: '' }, finish_reason: 'stop' }] })
      }
    })
  }

  for (const protocol of ['chat', 'responses', 'anthropic'] as const) {
    it(`holds ${protocol} after a delivered chunk until the stream gate releases`, async () => {
      const server = await startServer()
      const id = `chunk-gate-${protocol}`
      await registerRawScenario(server, id, { steps: [{ reasoning: 'THINKING', text: 'ANSWER', stream: { chunkChars: 4, delayMs: 0, gates: [{ afterChunk: 1, name: 'first-chunk' }] } }] })
      const prompt = mockScenarioPrompt(id, 'Stream the answer.')
      const path = protocol === 'chat' ? '/v1/chat/completions' : protocol === 'responses' ? '/v1/responses' : '/v1/messages'
      const body = protocol === 'responses' ? { stream: true, input: [{ role: 'user', content: prompt }] } : { stream: true, messages: [{ role: 'user', content: prompt }] }
      const response = await fetch(`${server.url}${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })
      expect(response.status).toBe(200)
      const reader = response.body!.getReader()
      const first = await reader.read()
      expect(first.done).toBe(false)
      const prefix = new TextDecoder().decode(first.value)
      expect(prefix).toContain('THIN')
      expect(prefix).not.toContain('ANSWER')
      expect(prefix).not.toContain('turn_ended')
      await waitForGate(server, id, 'first-chunk')
      expect((await fetch(`${server.url}/__e2e/scenarios/${id}/gates/first-chunk/release`, { method: 'POST' })).status).toBe(204)
      let rest = ''
      for (;;) {
        const next = await reader.read()
        if (next.done)
          break
        rest += new TextDecoder().decode(next.value)
      }
      expect(rest).toContain('KING')
      expect(rest).toContain('ANSW')
      expect((await readScenarioStatus(server.url, id)).pendingGates).toEqual([])
    })
  }

  // A model writes its tool call after the text that precedes it, so every native
  // API streams the text first. A client that receives the complete call first can
  // run it before the rest of the text arrives.
  for (const protocol of ['chat', 'responses', 'anthropic'] as const) {
    it(`sends the ${protocol} tool call only after the gated text`, async () => {
      const server = await startServer()
      const id = `tool-after-text-${protocol}`
      await registerRawScenario(server, id, { steps: [{
        text: 'FIRSTSECOND',
        toolCalls: [{ id: 'TOOLCALLID', name: 'TOOLNAME', arguments: { value: 'TOOLARGUMENT' } }],
        stream: { chunkChars: 5, delayMs: 0, gates: [{ afterChunk: 1, name: 'first-text' }] },
      }] })
      const prompt = mockScenarioPrompt(id, 'Stream the text, then call the tool.')
      const path = protocol === 'chat' ? '/v1/chat/completions' : protocol === 'responses' ? '/v1/responses' : '/v1/messages'
      const body = protocol === 'responses' ? { stream: true, input: [{ role: 'user', content: prompt }] } : { stream: true, messages: [{ role: 'user', content: prompt }] }
      const response = await fetch(`${server.url}${path}`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })
      expect(response.status).toBe(200)
      const reader = response.body!.getReader()
      let prefix = ''
      while (!prefix.includes('FIRST')) {
        const next = await reader.read()
        expect(next.done).toBe(false)
        prefix += new TextDecoder().decode(next.value)
      }
      await waitForGate(server, id, 'first-text')
      expect(prefix).not.toContain('TOOLCALLID')
      expect(prefix).not.toContain('TOOLNAME')
      expect(prefix).not.toContain('TOOLARGUMENT')
      expect((await fetch(`${server.url}/__e2e/scenarios/${id}/gates/first-text/release`, { method: 'POST' })).status).toBe(204)
      let rest = ''
      for (;;) {
        const next = await reader.read()
        if (next.done)
          break
        rest += new TextDecoder().decode(next.value)
      }
      expect(rest).toContain('SECON')
      expect(rest).toContain('TOOLCALLID')
      expect(rest.indexOf('SECON')).toBeLessThan(rest.indexOf('TOOLCALLID'))
      expect((await readScenarioStatus(server.url, id)).complete).toBe(true)
    })
  }

  it('streams a chat tool call with no text as one delta that carries the role', async () => {
    const server = await startServer()
    await registerScenario(server, 'tool-only-chat', { steps: [{ toolCalls: [{ id: 'ONLYCALL', name: 'only_tool', arguments: { value: 1 } }] }] })
    const response = await chat(server, mockScenarioPrompt('tool-only-chat', 'Call the tool.'))
    const deltas = (await responseText(response))
      .split('\n')
      .filter(line => line.startsWith('data: {'))
      .map(line => JSON.parse(line.slice(6)))
      .flatMap(chunk => chunk.choices.map((choice: { delta: Record<string, unknown>, finish_reason: string | null }) => choice))
    expect(deltas[0]?.delta).toEqual({
      role: 'assistant',
      tool_calls: [{ index: 0, id: 'ONLYCALL', type: 'function', function: { name: 'only_tool', arguments: '{"value":1}' } }],
    })
    expect(deltas.filter(choice => choice.delta.tool_calls !== undefined)).toHaveLength(1)
    expect(deltas.at(-1)).toMatchObject({ delta: {}, finish_reason: 'tool_calls' })
  })

  it('cancels a stream gate without sending later output or a completion', async () => {
    const server = await startServer()
    await registerRawScenario(server, 'cancel-stream-gate', { steps: [{ text: 'FIRSTSECOND', stream: { chunkChars: 5, delayMs: 0, gates: [{ afterChunk: 1, name: 'partial' }] } }] })
    const response = await chat(server, mockScenarioPrompt('cancel-stream-gate', 'Start then cancel.'))
    const reader = response.body!.getReader()
    const first = await reader.read()
    expect(new TextDecoder().decode(first.value)).toContain('FIRST')
    await waitForGate(server, 'cancel-stream-gate', 'partial')
    const pendingRead = reader.read()
    // The forced removal destroys the stream, so the pending read ends with the `terminated` error of fetch.
    const rejection = expect(pendingRead).rejects.toThrow('terminated')
    expect((await fetch(`${server.url}/__e2e/scenarios/cancel-stream-gate?force=true`, { method: 'DELETE' })).status).toBe(204)
    await rejection
  })

  for (const stream of [false, true]) {
    for (const input of ['', '*** Begin Patch\n*** Add File: example.txt\n+exact bytes\n*** End Patch']) {
      it(`preserves native custom input beside a function call with stream ${stream} and input length ${input.length}`, async () => {
        const server = await startServer()
        await registerScenario(server, 'native-custom', { steps: [{ toolCalls: [
          { id: 'patch-1', name: 'apply_patch', input },
          { id: 'view-2', name: 'view', arguments: { path: 'example.txt', start: 0 } },
        ] }] })
        const response = await chat(server, mockScenarioPrompt('native-custom', 'Apply and view.'), stream)
        const calls = stream
          ? (await responseText(response))
              .split('\n')
              .filter(line => line.startsWith('data: {'))
              .map(line => JSON.parse(line.slice(6)))
              .flatMap(chunk => chunk.choices.flatMap((choice: { delta: { tool_calls?: unknown[] } }) => choice.delta.tool_calls ?? []))
          : (await response.json()).choices[0].message.tool_calls
        expect(calls).toEqual([
          { ...(stream ? { index: 0 } : {}), id: 'patch-1', type: 'custom', custom: { name: 'apply_patch', input } },
          { ...(stream ? { index: 1 } : {}), id: 'view-2', type: 'function', function: { name: 'view', arguments: '{"path":"example.txt","start":0}' } },
        ])
      })
    }
  }

  it('records completed Cursor history beside the unchanged native body', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-history', { steps: [{ text: 'FIRST_ASSISTANT' }, { text: 'SECOND_ASSISTANT' }, { text: 'THIRD_ASSISTANT' }] })
    const first = mockScenarioPrompt('cursor-history', 'FIRST_USER')
    await cursorRun(server, 'history-id', first)
    await cursorRun(server, 'history-id', 'SECOND_USER')
    await cursorRun(server, 'history-id', 'THIRD_USER')
    const requests = (await readScenarioStatus(server.url, 'cursor-history')).requests
    expect(requests[0]).toHaveProperty('serverContext', { conversationId: 'history-id', messages: [] })
    expect(requests[1]).toMatchObject({
      body: { prompt: 'SECOND_USER', attachments: [], conversationId: 'history-id' },
      serverContext: { conversationId: 'history-id', messages: [
        { role: 'user', content: first },
        { role: 'assistant', content: 'FIRST_ASSISTANT' },
      ] },
    })
    expect(requests[1]?.body).toEqual({ prompt: 'SECOND_USER', attachments: [], conversationId: 'history-id' })
    expect(requests[1]?.nativeRequest).toEqual({ mode: 0, contextRules: [] })
    expect(requests[2]).toHaveProperty('serverContext', { conversationId: 'history-id', messages: [
      { role: 'user', content: first },
      { role: 'assistant', content: 'FIRST_ASSISTANT' },
      { role: 'user', content: 'SECOND_USER' },
      { role: 'assistant', content: 'SECOND_ASSISTANT' },
    ] })
  })

  it('separates interleaved Cursor conversation IDs inside one scenario', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-interleaved', { steps: [{ text: 'ANSWER_A' }, { text: 'ANSWER_B' }, { text: 'NEXT_A' }, { text: 'NEXT_B' }] })
    const promptA = mockScenarioPrompt('cursor-interleaved', 'USER_A')
    const promptB = mockScenarioPrompt('cursor-interleaved', 'USER_B')
    await cursorRun(server, 'id-a', promptA)
    await cursorRun(server, 'id-b', promptB)
    await cursorRun(server, 'id-a', 'NEXT_USER_A')
    await cursorRun(server, 'id-b', 'NEXT_USER_B')
    const requests = (await readScenarioStatus(server.url, 'cursor-interleaved')).requests
    expect(requests[1]).toHaveProperty('serverContext', { conversationId: 'id-b', messages: [] })
    expect(requests[2]).toHaveProperty('serverContext', { conversationId: 'id-a', messages: [{ role: 'user', content: promptA }, { role: 'assistant', content: 'ANSWER_A' }] })
    expect(requests[3]).toHaveProperty('serverContext', { conversationId: 'id-b', messages: [{ role: 'user', content: promptB }, { role: 'assistant', content: 'ANSWER_B' }] })
  })

  it('replaces Cursor history when another explicit scenario owns the same native ID', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-owner-a', { steps: [{ text: 'OWNER_A' }] })
    await registerScenario(server, 'cursor-owner-b', { steps: [{ text: 'OWNER_B' }] })
    await cursorRun(server, 'shared-id', mockScenarioPrompt('cursor-owner-a', 'A'))
    await cursorRun(server, 'shared-id', mockScenarioPrompt('cursor-owner-b', 'B'))
    expect((await readScenarioStatus(server.url, 'cursor-owner-b')).requests[0])
      .toHaveProperty('serverContext', { conversationId: 'shared-id', messages: [] })
  })

  it('keeps earlier Cursor history and excludes a cancelled turn', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-cancelled', { steps: [{ text: 'SAVED_ANSWER' }, { text: 'CANCELLED_ANSWER', gate: 'cancel-turn' }, { text: 'NEXT_ANSWER' }] })
    const first = mockScenarioPrompt('cursor-cancelled', 'SAVED_USER')
    await cursorRun(server, 'cancel-id', first)
    const controller = new AbortController()
    const held = cursorRun(server, 'cancel-id', 'CANCELLED_USER', controller.signal)
    const rejection = expect(held).rejects.toMatchObject({ name: 'AbortError' })
    await waitForGate(server, 'cursor-cancelled', 'cancel-turn')
    controller.abort()
    await rejection
    await cursorRun(server, 'cancel-id', 'NEXT_USER')
    expect((await readScenarioStatus(server.url, 'cursor-cancelled')).requests[2])
      .toHaveProperty('serverContext', { conversationId: 'cancel-id', messages: [{ role: 'user', content: first }, { role: 'assistant', content: 'SAVED_ANSWER' }] })
  })

  for (const force of [false, true]) {
    it(`clears Cursor context and routing after scenario removal with force ${force}`, async () => {
      const server = await startServer()
      await registerScenario(server, 'cursor-remove', { steps: [{ text: 'OLD' }] })
      await cursorRun(server, 'removed-id', mockScenarioPrompt('cursor-remove', 'OLD_USER'))
      expect((await fetch(`${server.url}/__e2e/scenarios/cursor-remove${force ? '?force=true' : ''}`, { method: 'DELETE' })).status).toBe(204)
      await registerScenario(server, AMBIENT_SCENARIO_ID, { steps: [{ text: 'NEW' }] })
      await cursorRun(server, 'removed-id', 'UNMARKED_USER')
      expect((await readScenarioStatus(server.url, AMBIENT_SCENARIO_ID)).requests[0])
        .toHaveProperty('serverContext', { conversationId: 'removed-id', messages: [] })
    })
  }

  it('keeps a native empty Cursor answer distinct from an interrupted turn', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-empty-answer', { steps: [{ text: '' }, { text: 'NEXT' }] })
    const first = mockScenarioPrompt('cursor-empty-answer', 'EMPTY_ANSWER_USER')
    await cursorRun(server, 'empty-id', first)
    await cursorRun(server, 'empty-id', 'NEXT_USER')
    expect((await readScenarioStatus(server.url, 'cursor-empty-answer')).requests[1])
      .toHaveProperty('serverContext', { conversationId: 'empty-id', messages: [{ role: 'user', content: first }, { role: 'assistant', content: '' }] })
  })

  it('adds no synthetic context to a Cursor Run without a native ID', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-no-id', { steps: [{ text: 'FIRST' }, { text: 'SECOND' }] })
    const prompt = mockScenarioPrompt('cursor-no-id', 'NO_ID_USER')
    await cursorRun(server, '', prompt)
    await cursorRun(server, '', prompt)
    for (const record of (await readScenarioStatus(server.url, 'cursor-no-id')).requests)
      expect(record).not.toHaveProperty('serverContext')
  })

  it('routes a bare Cursor command through its conversation script and clears that route', async () => {
    const server = await startServer()
    await registerScenario(server, 'cursor-conversation', { steps: [{ text: 'First answer.' }, { text: 'Command answer.' }] })
    await registerScenario(server, AMBIENT_SCENARIO_ID, { steps: [{ text: 'Ambient answer.' }] })
    await cursorRun(server, 'conversation-1', mockScenarioPrompt('cursor-conversation', 'Start this conversation.'))
    await cursorRun(server, 'conversation-1', '/compact')
    const scripted = await readScenarioStatus(server.url, 'cursor-conversation')
    expect(scripted.nextStep).toBe(2)
    expect(scripted.requests[1]?.body).toEqual({ prompt: '/compact', attachments: [], conversationId: 'conversation-1' })
    expect(scripted.requests[1]?.nativeRequest).toEqual({ mode: 0, contextRules: [] })

    const removed = await fetch(`${server.url}/__e2e/scenarios/cursor-conversation`, { method: 'DELETE' })
    expect(removed.status).toBe(204)
    await cursorRun(server, 'conversation-1', '/compact')
    expect((await readScenarioStatus(server.url, AMBIENT_SCENARIO_ID)).nextStep).toBe(1)
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

    const status = await readScenarioStatus(server.url, 'chat-text')
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
    expect(await readScenarioStatus(server.url, 'held-answer')).toMatchObject({ complete: true, pendingGates: [] })
    const duplicate = await fetch(`${server.url}/__e2e/scenarios/held-answer/gates/child-answer/release`, { method: 'POST' })
    expect(duplicate.status).toBe(409)
  })

  it('cancels a held answer when the scenario is removed', async () => {
    const server = await startServer()
    await registerScenario(server, 'abandoned-gate', { steps: [{ text: 'Never sent.', gate: 'child-answer' }] })
    const pending = chat(server, mockScenarioPrompt('abandoned-gate', 'Ask the child.'), false)
    // The forced removal destroys the held exchange before its headers, so fetch itself fails.
    const rejection = expect(pending).rejects.toThrow('fetch failed')
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
      capabilities: { supports: { reasoning_effort: ['low', 'high'] } },
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
    expect((await readScenarioStatus(server.url, 'droid-whoami')).nextStep).toBe(0)
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

    expect(await readScenarioStatus(server.url, 'unstreamed')).toMatchObject({ complete: true })
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
    expect(await readScenarioStatus(server.url, 'older')).toMatchObject({ nextStep: 0 })
    expect(await readScenarioStatus(server.url, 'newer')).toMatchObject({ nextStep: 1 })
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

    const status = await readScenarioStatus(server.url, 'ruled')
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
    expect(await readScenarioStatus(server.url, 'once')).toMatchObject({ complete: true, ruleMatches: { greeting: 1 } })
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
    expect(await readScenarioStatus(server.url, 'spare')).toMatchObject({ nextStep: 0 })

    const status = await readScenarioStatus(server.url, 'one-step')
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toHaveLength(1)
    expect(status.unexpectedRequests[0]).toMatchObject({
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      reason: 'The scenario has no remaining scripted answer.',
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
    const status = await readScenarioStatus(server.url, 'captured')
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toMatchObject([{ reason: 'Capture planFile matched nothing in the request.' }])
  })

  it('records a request that reaches no scenario, with the body that arrived', async () => {
    const server = await startServer()
    expect((await chat(server, 'No marker at all')).status).toBe(409)
    expect((await chat(server, mockScenarioPrompt('missing', 'Unknown scenario.'))).status).toBe(409)

    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.unmatched).toMatchObject([
      { scenarioID: 'ambient', reason: 'The scenario is not registered.', body: { messages: [{ content: 'No marker at all' }] } },
      { scenarioID: 'missing', reason: 'The scenario is not registered.' },
    ])
    expect(server.ancestorInstructionRequests()).toBe(0)
  })

  it('refuses a request that holds the text of an instruction file above the working directory, and counts it', async () => {
    const server = await startServer()
    await registerScenario(server, 'leaky', { steps: [{ text: 'The script never sends this answer.' }] })
    expect((await chat(server, mockScenarioPrompt('leaky', `Context: ${ANCESTOR_INSTRUCTION_SENTINEL}`))).status).toBe(409)
    expect((await chat(server, `No marker. Context: ${ANCESTOR_INSTRUCTION_SENTINEL}`)).status).toBe(409)

    // The marked request fails its scenario, and the script keeps its step.
    const status = await readScenarioStatus(server.url, 'leaky')
    expect(status.nextStep).toBe(0)
    expect(status.complete).toBe(false)
    expect(status.unexpectedRequests).toMatchObject([{ reason: expect.stringContaining(ANCESTOR_INSTRUCTION_SENTINEL) }])
    // The unmarked request fails no test, so the log keeps it and the count makes the suite fail at shutdown.
    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.unmatched).toMatchObject([{ scenarioID: AMBIENT_SCENARIO_ID, reason: expect.stringContaining(ANCESTOR_INSTRUCTION_SENTINEL) }])
    expect(server.ancestorInstructionRequests()).toBe(2)
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
    await expect(aborted).rejects.toMatchObject({ name: 'AbortError' })

    // The step counts as consumed: the agent asked for it and the server chose it.
    expect(await readScenarioStatus(server.url, 'interrupted')).toMatchObject({ complete: true, nextStep: 1 })
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
      error: { message: expect.stringContaining('needs text, reasoning, toolCalls, or error') },
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

  it('atomically allows an unused queue when the scenario has no unexpected request', async () => {
    const server = await startServer()
    await registerScenario(server, 'allowed-unused-queue', { steps: [{ text: 'This answer stays unused.' }] })
    const removed = await fetch(`${server.url}/__e2e/scenarios/allowed-unused-queue?allow-unconsumed=true`, { method: 'DELETE' })
    expect(removed.status).toBe(204)
    expect((await fetch(`${server.url}/__e2e/scenarios/allowed-unused-queue`)).status).toBe(404)
  })

  it('cancels an actually held model response after an allowed unused-queue deletion', async () => {
    const server = await startServer()
    const id = 'allowed-held-response'
    await registerScenario(server, id, { steps: [{ text: 'This response stays held.', gate: 'native-held-delete' }] })
    const pending = chat(server, mockScenarioPrompt(id, 'Hold this native response.'), false)
      .then(response => response.text())
      .then(() => ({ state: 'answered' }), (error: unknown) => ({ state: 'cancelled', error }))
    await waitForGate(server, id, 'native-held-delete')
    const removed = await fetch(`${server.url}/__e2e/scenarios/${id}?allow-unconsumed=true`, { method: 'DELETE' })
    expect(removed.status).toBe(204)
    expect(await pending).toMatchObject({ state: 'cancelled', error: expect.any(Error) })
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}`)).status).toBe(404)
  })

  it('refuses an unexpected request under the unused-queue policy until forced cleanup', async () => {
    const server = await startServer()
    const id = 'refused-unused-queue'
    await registerScenario(server, id, { steps: [{ text: 'The first native turn has a scripted answer.' }] })
    const first = await chat(server, mockScenarioPrompt(id, 'The scripted native turn arrives.'), false)
    expect(first.status).toBe(200)
    await first.arrayBuffer()
    const unexpected = await chat(server, mockScenarioPrompt(id, 'This native turn has no scripted answer.'), false)
    expect(unexpected.status).toBe(409)
    await unexpected.arrayBuffer()
    const extended = await fetch(`${server.url}/__e2e/scenarios/${id}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ steps: [{ text: 'The interrupted turn never requests this answer.' }] }),
    })
    expect(extended.status).toBe(200)
    const refused = await fetch(`${server.url}/__e2e/scenarios/${id}?allow-unconsumed=true`, { method: 'DELETE' })
    expect(refused.status).toBe(409)
    const status = await refused.json() as MockModelScenarioStatus
    expect(status).toMatchObject({ nextStep: 1, stepCount: 2 })
    expect(status.unexpectedRequests).toHaveLength(1)
    expect(status.unexpectedRequests[0]).toMatchObject({ protocol: 'openai-chat-completions', reason: 'The scenario has no remaining scripted answer.' })
    expect((await readScenarioStatus(server.url, id)).unexpectedRequests).toHaveLength(1)
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}?force=true`, { method: 'DELETE' })).status).toBe(204)
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}`)).status).toBe(404)
  })

  it('refuses contradictory deletion policies without removing the scenario', async () => {
    const server = await startServer()
    const id = 'contradictory-delete-policy'
    await registerScenario(server, id, { steps: [{ text: 'This answer stays unused.' }] })
    const refused = await fetch(`${server.url}/__e2e/scenarios/${id}?force=true&allow-unconsumed=true`, { method: 'DELETE' })
    expect(refused.status).toBe(400)
    expect(await refused.json()).toEqual({ error: { message: 'Scenario deletion cannot combine force and allow-unconsumed.' } })
    expect(await readScenarioStatus(server.url, id)).toMatchObject({ nextStep: 0, stepCount: 1 })
    expect((await fetch(`${server.url}/__e2e/scenarios/${id}?force=true`, { method: 'DELETE' })).status).toBe(204)
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
    const status = await readScenarioStatus(server.url, 'amp-text')
    expect(status).toMatchObject({ complete: true, nextStep: 1 })
    expect(status.requests[0]).toMatchObject({ protocol: 'anthropic-messages', stepIndex: 0 })
    socket.close()
  })

  describe('with a run root', () => {
    let runRoot: string

    beforeEach(() => {
      const scratch = resolve(import.meta.dirname, '../../../../.tmp')
      mkdirSync(scratch, { recursive: true })
      runRoot = mkdtempSync(join(scratch, 'mock-run-root-'))
    })

    afterEach(() => rmSync(runRoot, { recursive: true, force: true }))

    /** Send one guidance snapshot to a thread whose first message marks `scenario`, after the scenario answered it. */
    async function sendGuidance(server: MockModelServer, scenario: string, files: unknown) {
      const created = await (await fetch(`${server.url}/api/thread-actors`, { method: 'POST', body: '{}' })).json() as { threadId: string }
      const { socket, frames } = await actorSocket(server, created.threadId)
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'client_append_user_msg', params: { content: [{ type: 'text', text: mockScenarioPrompt(scenario, 'Read the guidance.') }] } }))
      await expect.poll(() => frames.some(frame => JSON.stringify(frame).includes('Guidance answer'))).toBe(true)
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'executor_guidance_snapshot', params: { snapshotId: 'snapshot', files, isLast: true } }))
      await expect.poll(() => frames.some(frame => isObject(frame) && frame.id === 2)).toBe(true)
      socket.close()
    }

    it('refuses a run root that is not an absolute path to a directory', async () => {
      await expect(createMockModelServer({ models: MOCK_MODEL_IDS, runRoot: 'relative/root' })).rejects.toThrow('must be an absolute path to a directory')
      await expect(createMockModelServer({ models: MOCK_MODEL_IDS, runRoot: join(runRoot, 'absent') })).rejects.toThrow('must be an absolute path to a directory')
      writeFileSync(join(runRoot, 'file'), '')
      await expect(createMockModelServer({ models: MOCK_MODEL_IDS, runRoot: join(runRoot, 'file') })).rejects.toThrow('must be an absolute path to a directory')
    })

    it('refuses an Amp guidance file outside the run root, or one that holds the sentinel, in the scenario of its thread, and counts it', async () => {
      const server = await createMockModelServer({ models: MOCK_MODEL_IDS, runRoot })
      servers.push(server)
      for (const id of ['amp-outside', 'amp-sentinel', 'amp-project'])
        await registerScenario(server, id, { steps: [{ text: 'Guidance answer' }] })
      await sendGuidance(server, 'amp-outside', [{ uri: pathToFileURL(join(dirname(runRoot), 'AGENTS.md')).href, content: 'A file above the run root.' }])
      await sendGuidance(server, 'amp-sentinel', [{ uri: pathToFileURL(join(runRoot, 'AGENTS.md')).href, content: ANCESTOR_INSTRUCTION_SENTINEL }])
      await sendGuidance(server, 'amp-project', [{ uri: pathToFileURL(join(runRoot, '1', 'work', 'AGENTS.md')).href, content: 'Project guidance.' }])
      await expect.poll(() => server.ancestorInstructionRequests()).toBe(2)
      expect((await readScenarioStatus(server.url, 'amp-outside')).unexpectedRequests).toMatchObject([{ reason: expect.stringContaining(join(dirname(runRoot), 'AGENTS.md')) }])
      expect((await readScenarioStatus(server.url, 'amp-sentinel')).unexpectedRequests).toMatchObject([{ reason: expect.stringContaining(ANCESTOR_INSTRUCTION_SENTINEL) }])
      expect((await readScenarioStatus(server.url, 'amp-project')).unexpectedRequests).toEqual([])
    })

    it.each([
      { label: 'an invalid entry', files: [null] },
      { label: 'an invalid list', files: 'file:///outside/AGENTS.md' },
    ])('refuses $label in an Amp guidance snapshot and counts the refusal', async ({ files }) => {
      const server = await createMockModelServer({ models: MOCK_MODEL_IDS, runRoot })
      servers.push(server)
      await registerScenario(server, 'amp-invalid-guidance', { steps: [{ text: 'Guidance answer' }] })
      await sendGuidance(server, 'amp-invalid-guidance', files)
      expect(server.ancestorInstructionRequests()).toBe(1)
      const status = await readScenarioStatus(server.url, 'amp-invalid-guidance')
      expect(status.unexpectedRequests).toHaveLength(1)
      expect(status.unexpectedRequests[0]?.reason).toContain('instruction files that its test does not control')
    })

    it('refuses a Cursor rule outside the run root in the scenario of its turn, and lets the sentinel files of the run root through', async () => {
      const server = await createMockModelServer({ models: MOCK_MODEL_IDS, runRoot })
      servers.push(server)
      for (const id of ['cursor-outside', 'cursor-escape'])
        await registerScenario(server, id, { steps: [{ text: 'Rules answer' }] })
      const outside = join(dirname(runRoot), '.cursor', 'rules', 'outside.mdc')
      await cursorRun(server, 'outside-conversation', mockScenarioPrompt('cursor-outside', 'Load the rules.'), undefined, [{ path: outside, content: 'An outside rule.' }])
      await cursorRun(server, 'escape-conversation', mockScenarioPrompt('cursor-escape', 'Load the rules.'), undefined, [{ path: join(runRoot, 'AGENTS.md'), content: ANCESTOR_INSTRUCTION_SENTINEL }])
      expect(server.ancestorInstructionRequests()).toBe(1)
      expect((await readScenarioStatus(server.url, 'cursor-outside')).unexpectedRequests).toMatchObject([{ reason: expect.stringContaining(outside) }])
      const escape = await readScenarioStatus(server.url, 'cursor-escape')
      expect(escape.unexpectedRequests).toEqual([])
      expect(escape.requests[0]?.nativeRequest?.contextRules).toEqual([{ path: join(runRoot, 'AGENTS.md'), content: ANCESTOR_INSTRUCTION_SENTINEL }])
    })

    it('logs a refused guidance file of a thread that no scenario marks', async () => {
      const server = await createMockModelServer({ models: MOCK_MODEL_IDS, runRoot })
      servers.push(server)
      const created = await (await fetch(`${server.url}/api/thread-actors`, { method: 'POST', body: '{}' })).json() as { threadId: string }
      const { socket, frames } = await actorSocket(server, created.threadId)
      socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'executor_guidance_snapshot', params: { files: [{ uri: 'file:///AGENTS.md', content: 'The root of the file system.' }] } }))
      await expect.poll(() => frames.some(frame => isObject(frame) && frame.id === 1)).toBe(true)
      socket.close()
      expect(server.ancestorInstructionRequests()).toBe(1)
      const log = await (await fetch(`${server.url}/__e2e/requests`)).json() as { unmatched: { scenarioID: string, reason: string }[] }
      expect(log.unmatched).toMatchObject([{ scenarioID: AMBIENT_SCENARIO_ID, reason: expect.stringContaining('/AGENTS.md') }])
    })
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

describe('Cursor delivered response receipts', () => {
  it.each([
    { id: 'cursor-invalid-receipt', error: { status: 400, message: 'The native invalid request.' }, code: 'invalid_argument' },
    { id: 'cursor-default-receipt', error: { status: 500, message: 'The native internal failure.' }, code: 'internal' },
    { id: 'cursor-quota-receipt', error: { status: 429, code: 'rate_limit_exceeded', message: 'The native quota failure.' }, code: 'resource_exhausted' },
    { id: 'cursor-unavailable-receipt', error: { status: 503, message: 'The native service is unavailable.' }, code: 'unavailable' },
  ])('records the actual delivered Connect error for $id', async ({ id, error, code }) => {
    const server = await startServer()
    await registerScenario(server, id, { steps: [{ error }] })
    const response = await cursorResponse(server, `${id}-conversation`, mockScenarioPrompt(id, 'Run the native error receipt test.'))
    expect(response.status).toBe(200)
    const body = new Uint8Array(await response.arrayBuffer())
    const frames = takeConnectFrames(body)
    expect(frames.rest).toHaveLength(0)
    const trailer = frames.frames.at(-1)
    if (!trailer)
      throw new Error('The actual Cursor response contains no end-of-stream frame.')
    expect(trailer.flags).toBe(2)
    const wire = JSON.parse(new TextDecoder().decode(trailer.payload)) as { error: { code: string, message: string } }
    expect(wire.error).toMatchObject({ code, message: error.message })
    await expect.poll(async () => (await readScenarioStatus(server.url, id)).requests[0]?.response !== undefined).toBe(true)
    const receipt = (await readScenarioStatus(server.url, id)).requests[0]?.response
    expect(receipt).toMatchObject({ status: response.status, serviceError: { code: wire.error.code, message: wire.error.message } })
    expect(receipt?.serviceError?.code).not.toBe('api_error')
  })

  // Every surface holds a step at its gate before it answers, an error included.
  it('holds a gated error step until its test releases the gate', async () => {
    const server = await startServer()
    const id = 'cursor-gated-error'
    await registerScenario(server, id, { steps: [{ error: { status: 503, message: 'The held native failure.' }, gate: 'held-error' }] })
    const pending = cursorResponse(server, `${id}-conversation`, mockScenarioPrompt(id, 'Run the held native error.'))
    await waitForGate(server, id, 'held-error')
    const release = await fetch(`${server.url}/__e2e/scenarios/${id}/gates/held-error/release`, { method: 'POST' })
    expect(release.status).toBe(204)
    const trailer = takeConnectFrames(new Uint8Array(await (await pending).arrayBuffer())).frames.at(-1)
    expect(trailer?.flags).toBe(2)
    expect(JSON.parse(new TextDecoder().decode(trailer?.payload))).toMatchObject({ error: { code: 'unavailable', message: 'The held native failure.' } })
  })
})

describe('allowlisted native request headers', () => {
  it('records the actual beta header and excludes credentials and unrelated headers', async () => {
    const server = await startServer()
    const id = 'anthropic-beta-header-receipt'
    const secret = 'THIS_HEADER_SECRET_MUST_NOT_ENTER_A_MODEL_RECORD'
    await registerScenario(server, id, { steps: [{ text: 'The beta request completed.' }] })
    const response = await fetch(`${server.url}/v1/messages`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'x-api-key': MODEL_KEY, 'Anthropic-Beta': 'context-1m-2025-08-07,interleaved-thinking-2025-05-14', 'x-private-secret': secret, 'cookie': `private_session=${secret}` },
      body: JSON.stringify({ model: 'mock-model', max_tokens: 64, stream: false, messages: [{ role: 'user', content: mockScenarioPrompt(id, 'Record the actual allowlisted beta header.') }] }),
    })
    expect(response.status).toBe(200)
    await response.arrayBuffer()
    const record = (await readScenarioStatus(server.url, id)).requests[0]
    expect(record).toMatchObject({ requestHeaders: { 'anthropic-beta': 'context-1m-2025-08-07,interleaved-thinking-2025-05-14' } })
    if (!record || !('requestHeaders' in record) || !isObject(record.requestHeaders))
      throw new Error('The native request has no object header receipt.')
    expect(Object.keys(record.requestHeaders)).toEqual(['anthropic-beta'])
    expect(JSON.stringify(record)).not.toContain(secret)
    expect(JSON.stringify(record)).not.toContain(MODEL_KEY)
  })

  it('omits absent beta headers and preserves an actual empty value in the same scenario', async () => {
    const server = await startServer()
    const id = 'anthropic-beta-header-boundaries'
    await registerScenario(server, id, { steps: [{ text: 'No beta header.' }, { text: 'An empty beta header.' }] })
    for (const [index, beta] of [undefined, ''].entries()) {
      const response = await fetch(`${server.url}/v1/messages`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'x-api-key': MODEL_KEY, ...(beta === undefined ? {} : { 'anthropic-beta': beta }) },
        body: JSON.stringify({ model: 'mock-model', max_tokens: 64, stream: false, messages: [{ role: 'user', content: mockScenarioPrompt(id, `Record beta header boundary ${index}.`) }] }),
      })
      expect(response.status).toBe(200)
      await response.arrayBuffer()
    }
    const records = (await readScenarioStatus(server.url, id)).requests
    expect(records).toHaveLength(2)
    expect(records[0]).not.toHaveProperty('requestHeaders')
    expect(records[1]).toMatchObject({ requestHeaders: { 'anthropic-beta': '' } })
  })
})

describe('native Surface dispatch and accounting', () => {
  it('serves Qoder config and its event stream without consuming an ordered model turn', async () => {
    const server = await startServer()
    const scenario = 'qoder-config-accounting'
    await registerScenario(server, scenario, { steps: [{ text: 'Only the actual model request consumes this.' }] })
    const exchange = await fetch(`${server.url}/api/v1/jobToken/exchange`, { method: 'POST' })
    expect(exchange.status).toBe(200)
    const credential: unknown = await exchange.json()
    if (!isObject(credential) || typeof credential.token !== 'string')
      throw new Error('The isolated Qoder token exchange returned no token.')
    const headers = { 'authorization': `Bearer ${credential.token}`, 'content-type': 'application/json' }
    const response = await fetch(`${server.url}/api/v1/qcs/config/resolve`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ namespaces: ['qodercli-feature-gates'], keys: [], context: { platform: 'macos', clientVersion: '1.1.65', clientType: 'qodercli' } }),
    })
    expect(response.status).toBe(200)
    expect(response.headers.get('etag')).toBe('"leapmux-e2e-qoder-config"')
    expect(await response.json()).toMatchObject({ configs: { 'qodercli-feature-gates': { prompt_policy: { scope: 'default' } } } })
    const controller = new AbortController()
    const stream = await fetch(`${server.url}/api/v1/qcs/config/stream?ns=qodercli-feature-gates`, { headers, signal: controller.signal })
    expect(stream.status).toBe(200)
    const reader = stream.body?.getReader()
    if (!reader)
      throw new Error('The isolated Qoder stream returned no reader.')
    try {
      const connected = await reader.read()
      expect(connected.done).toBe(false)
      expect(new TextDecoder().decode(connected.value)).toBe('event: connected\ndata: {}\n\n')
      expect(await readScenarioStatus(server.url, scenario)).toMatchObject({ nextStep: 0, requests: [], unexpectedRequests: [] })
    }
    finally {
      await reader.cancel()
      controller.abort()
    }
    expect(await responseText(await chat(server, mockScenarioPrompt(scenario, 'Run the actual model request.')))).toContain('Only the actual model request consumes this.')
    expect(await readScenarioStatus(server.url, scenario)).toMatchObject({ complete: true, nextStep: 1 })
  })

  it('serves exact Qoder API routes before the broad Amp API prefix without consuming model steps', async () => {
    const server = await startServer()
    await registerScenario(server, 'surface-startup-accounting', { steps: [{ text: 'Only the actual model turn consumes this.' }] })
    const expected = [
      { path: '/api/v1/userinfo', method: 'GET', body: { uid: 'leapmux-e2e', email: 'e2e@leapmux.test' } },
      { path: '/api/v3/user/status', method: 'GET', body: { featureSwitches: { allow_byok: 2 } } },
      { path: '/api/v2/user/plan', method: 'PATCH', body: { code: 0, success: true, data: {} } },
      { path: '/ide-text/latest', method: 'GET', body: { code: 0, success: true, data: {} } },
      { path: '/algo/api/v2/model/list', method: 'GET', body: { code: 0, success: true, data: {} } },
    ]
    for (const { path, method, body } of expected) {
      const response = await fetch(`${server.url}${path}`, { method })
      expect(response.status).toBe(200)
      expect(await response.json()).toMatchObject(body)
    }
    for (const path of ['/api/v1/jobToken/exchange', '/api/v1/jobToken/refresh']) {
      const response = await fetch(`${server.url}${path}`, { method: 'POST' })
      expect(response.status).toBe(200)
      expect(await response.json()).toMatchObject({ token: 'leapmux-e2e-token', access_token: 'leapmux-e2e-token', refresh_token: 'leapmux-e2e-token' })
    }
    expect(await readScenarioStatus(server.url, 'surface-startup-accounting')).toMatchObject({ nextStep: 0, requests: [], unexpectedRequests: [] })
    const ampUser = await fetch(`${server.url}/api/internal?getUserInfo`)
    expect(ampUser.status).toBe(200)
    expect(await ampUser.json()).toMatchObject({ ok: true, result: { id: 'user_leapmux_e2e' } })
    expect(await readScenarioStatus(server.url, 'surface-startup-accounting')).toHaveProperty('nextStep', 0)
  })

  it('retains both Qoder v5 discovery shapes and its v3 string URLs on the actual listening origin', async () => {
    const server = await startServer()
    const discovery: { path: string, headers: Record<string, string>, node: string | { url: string } }[] = [
      { path: '/algo/api/v3/service/region/endpoints', headers: {}, node: server.url },
      { path: '/algo/api/v5/service/region/endpoints', headers: {}, node: { url: server.url } },
      { path: '/algo/api/v5/service/region/endpoints', headers: { 'cosy-machineid': '' }, node: server.url },
    ]
    for (const { path, headers, node } of discovery) {
      const response = await fetch(`${server.url}${path}`, { headers })
      expect(response.status).toBe(200)
      expect(await response.json()).toEqual({ centerNodes: [node], inferNodes: [node], security: [node], openapiNodes: [node] })
    }
    const log = await fetch(`${server.url}/__e2e/requests`).then(response => response.json())
    expect(log.unmatched).toEqual([])
  })

  it('keeps identity and native startup traffic separate from every ordered model queue', async () => {
    const server = await startServer()
    await registerScenario(server, 'surface-identity-accounting', { steps: [{ text: 'Actual ordered answer.' }] })
    const startup: { path: string, method: string, headers: Record<string, string> }[] = [
      { path: '/v1/api/cli/whoami', method: 'GET', headers: { authorization: `Bearer ${MODEL_KEY}` } },
      { path: '/copilot_internal/user', method: 'GET', headers: {} },
      { path: '/user', method: 'GET', headers: {} },
      { path: '/auto', method: 'POST', headers: {} },
      { path: '/models', method: 'GET', headers: {} },
      { path: '/aiserver.v1.AiService/GetUserSettings', method: 'POST', headers: { 'content-type': 'application/json' } },
    ]
    for (const { path, method, headers } of startup) {
      const response = await fetch(`${server.url}${path}`, { method, headers })
      expect(response.status).toBe(200)
      await response.arrayBuffer()
    }
    expect(await readScenarioStatus(server.url, 'surface-identity-accounting')).toMatchObject({ nextStep: 0, requests: [], unexpectedRequests: [] })
    expect(await responseText(await chat(server, mockScenarioPrompt('surface-identity-accounting', 'Actual model content.')))).toContain('Actual ordered answer.')
    expect(await readScenarioStatus(server.url, 'surface-identity-accounting')).toMatchObject({ complete: true, nextStep: 1 })
  })
})

describe('native Copilot credential receipts', () => {
  it('records the exact isolated GitHub bearer from the actual native CAPI model path without its value', async () => {
    const server = await startServer()
    await registerScenario(server, 'copilot-fixed-bearer-receipt', { steps: [{ text: 'The isolated native bearer reaches the mock.' }] })
    const response = await fetch(`${server.url}/chat/completions`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', 'authorization': `Bearer ${MOCK_COPILOT_GITHUB_TOKEN}` },
      body: JSON.stringify({ model: MOCK_MODELS.openai, stream: false, messages: [{ role: 'user', content: mockScenarioPrompt('copilot-fixed-bearer-receipt', 'Complete one native-shaped model request.') }] }),
    })
    expect(response.status).toBe(200)
    expect(await response.text()).toContain('The isolated native bearer reaches the mock.')
    const status = await readScenarioStatus(server.url, 'copilot-fixed-bearer-receipt')
    expect(status).toMatchObject({ complete: true, nextStep: 1, unexpectedRequests: [] })
    expect(status.requests).toHaveLength(1)
    expect(status.requests[0]).toMatchObject({ protocol: 'openai-chat-completions', path: '/chat/completions', stepIndex: 0, mockCredential: { kind: 'bearer', accepted: true } })
    expect(Object.keys(status.requests[0]?.mockCredential ?? {}).sort()).toEqual(['accepted', 'kind'])
    expect(JSON.stringify(status)).not.toContain(MOCK_COPILOT_GITHUB_TOKEN)
  })
})
