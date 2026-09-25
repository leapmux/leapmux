import type { AddressInfo } from 'node:net'
import type { Duplex } from 'node:stream'
import type { AmpInference, AmpSurface } from './ampSurface'
import type { MockModelStep } from './mockModelScript'
import { createServer } from 'node:http'
import { afterEach, describe, expect, it } from 'vitest'
import { AMP_E2E_THREADS_PATH, ampInferenceOf, ampMessageID, ampToolUseID, createAmpSurface, isAmpPath } from './ampSurface'

const ID_PATTERN = { message: /^M-[0-9A-Z]{22}$/i, toolUse: /^TU-[0-9A-Z]{22}$/i }

/** An answer that the test holds open, and settles when it chooses. */
function heldAnswer() {
  let settle!: (step: MockModelStep | undefined) => void
  const promise = new Promise<MockModelStep | undefined>((resolve) => {
    settle = resolve
  })
  return { promise, settle }
}

/**
 * A mock whose answers come from a list the test controls, with every inference
 * recorded. A promise in the list holds its inference open until the test settles it.
 */
async function startMock(answers: (MockModelStep | undefined | Promise<MockModelStep | undefined>)[]) {
  const inferences: AmpInference[] = []
  const surface: AmpSurface = createAmpSurface({
    answer: async (inference) => {
      inferences.push(inference)
      return answers.shift()
    },
  })
  const sockets: Duplex[] = []
  const server = createServer((request, response) => {
    void surface.handleHttp(request, response, new URL(request.url ?? '/', 'http://127.0.0.1'))
  })
  server.on('upgrade', (request, socket, head) => {
    sockets.push(socket)
    surface.handleUpgrade(request, socket, head, new URL(request.url ?? '/', 'http://127.0.0.1'))
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo
  const origin = `http://127.0.0.1:${port}`
  return {
    origin,
    inferences,
    post: async (path: string, body: unknown) => (await fetch(`${origin}${path}`, { method: 'POST', body: JSON.stringify(body) })).json() as Promise<Record<string, unknown>>,
    close: async () => {
      surface.close()
      for (const socket of sockets)
        socket.destroy()
      server.closeAllConnections()
      await new Promise<void>(resolve => server.close(() => resolve()))
    },
  }
}

type Mock = Awaited<ReturnType<typeof startMock>>
let mock: Mock | undefined

afterEach(async () => {
  await mock?.close()
  mock = undefined
})

/** One socket of the CLI, with every frame it received. */
class CliSocket {
  readonly frames: unknown[] = []
  private nextID = 0

  private constructor(readonly socket: WebSocket) {
    socket.addEventListener('message', (event) => {
      const text = String(event.data)
      this.frames.push(text === 'pong' ? 'pong' : JSON.parse(text))
    })
  }

  static async open(origin: string, threadID: string): Promise<CliSocket> {
    const url = `${origin.replace('http:', 'ws:')}/actors/gateway/threadActor/websocket/?rvt-namespace=default&rvt-method=get&rvt-key=${threadID}`
    const socket = new WebSocket(url, ['rivet', 'rivet_encoding.bare'])
    const cli = new CliSocket(socket)
    await new Promise<void>(resolve => socket.addEventListener('open', () => resolve(), { once: true }))
    await expect.poll(() => cli.frames[0]).toBe('pong')
    return cli
  }

  request(method: string, params: Record<string, unknown> = {}): string {
    const id = `client-${this.nextID++}`
    this.socket.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }))
    return id
  }

  /** The notifications of one method, in arrival order. */
  notifications(method: string): Record<string, unknown>[] {
    return this.frames
      .filter((frame): frame is Record<string, unknown> => typeof frame === 'object' && frame !== null && (frame as Record<string, unknown>).method === method)
      .map(frame => frame.params as Record<string, unknown>)
  }

  /** The message of each `message_added` notification. */
  messages(): Record<string, unknown>[] {
    return this.notifications('message_added').map(params => params.message as Record<string, unknown>)
  }

  /** The result of the reply to one request, or undefined before the reply arrives. */
  reply(id: string): unknown {
    const frame = this.frames.find((candidate): candidate is Record<string, unknown> =>
      typeof candidate === 'object' && candidate !== null && !Array.isArray(candidate) && (candidate as Record<string, unknown>).id === id)
    return frame?.result
  }

  close(): void {
    this.socket.close()
  }
}

/** A thread with a client socket and an executor socket, as the CLI opens it. */
async function openThread(current: Mock): Promise<{ threadID: string, client: CliSocket, executor: CliSocket }> {
  const created = await current.post('/api/thread-actors', { agentMode: 'high' })
  const threadID = String(created.threadId)
  const executor = await CliSocket.open(current.origin, threadID)
  executor.request('executor_connect', { clientId: 'amp-x-e2e' })
  executor.request('executor_environment_snapshot', { environment: { trees: [{ displayName: 'work', uri: 'file:///work' }] } })
  const client = await CliSocket.open(current.origin, threadID)
  client.request('client_resume', {})
  await expect.poll(() => executor.notifications('executor_connected').length).toBe(1)
  return { threadID, client, executor }
}

const userMessage = (text: string, extra: Record<string, unknown> = {}) => ({ content: [{ type: 'text', text }], messageId: ampMessageID(), ...extra })

describe('amp ids', () => {
  it('spells a message id and a tool-use id the way the protocol validates them', () => {
    expect(ampMessageID()).toMatch(ID_PATTERN.message)
    expect(ampToolUseID('T-1', 'call-1')).toMatch(ID_PATTERN.toolUse)
  })

  it('derives the same tool-use id for the same call, and another for another', () => {
    expect(ampToolUseID('T-1', 'call-1')).toBe(ampToolUseID('T-1', 'call-1'))
    expect(ampToolUseID('T-1', 'call-1')).not.toBe(ampToolUseID('T-1', 'call-2'))
    expect(ampToolUseID('T-1', 'call-1')).not.toBe(ampToolUseID('T-2', 'call-1'))
  })
})

describe('isAmpPath', () => {
  it('claims Amp\'s REST routes, the actor gateway and the test route, and nothing of a model API', () => {
    expect(isAmpPath('/api/internal')).toBe(true)
    expect(isAmpPath('/api/thread-actors')).toBe(true)
    expect(isAmpPath('/actors/gateway/threadActor/websocket/')).toBe(true)
    expect(isAmpPath(AMP_E2E_THREADS_PATH)).toBe(true)
    for (const path of ['/v1/messages', '/v1/chat/completions', '/copilot_internal/user', '/agent.v1.AgentService/Run'])
      expect(isAmpPath(path), path).toBe(false)
  })
})

describe('ampInferenceOf', () => {
  it('states the conversation as an Anthropic Messages body, with each run record as text', () => {
    const inference = ampInferenceOf([
      { role: 'user', content: [{ type: 'text', text: 'LEAPMUXE2ESCENARIO:abc run ls' }] },
      { role: 'assistant', content: [{ type: 'thinking', thinking: 'Plan.', blockState: 'complete' }, { type: 'tool_use', id: 'TU-1', name: 'shell_command', input: { command: 'ls' }, complete: true }] },
      { role: 'user', content: [{ type: 'tool_result', toolUseID: 'TU-1', run: { status: 'done', result: { output: 'a', exitCode: 0 } } }] },
    ])
    expect(inference.body.messages).toEqual([
      { role: 'user', content: [{ type: 'text', text: 'LEAPMUXE2ESCENARIO:abc run ls' }] },
      { role: 'assistant', content: [{ type: 'thinking', thinking: 'Plan.' }, { type: 'tool_use', id: 'TU-1', name: 'shell_command', input: { command: 'ls' } }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'TU-1', content: '{"status":"done","result":{"output":"a","exitCode":0}}' }] },
    ])
    expect(inference.userText).toBe('{"status":"done","result":{"output":"a","exitCode":0}}')
  })

  it('states an empty conversation', () => {
    expect(ampInferenceOf([])).toEqual({ body: { model: 'mock-model', system: '', messages: [] }, userText: '' })
  })
})

describe('the Amp REST surface', () => {
  it('creates a thread with its mode, and continues the one a request states', async () => {
    mock = await startMock([])
    const created = await mock.post('/api/thread-actors', { agentMode: 'ultra' })
    expect(String(created.threadId)).toMatch(/^T-[0-9a-f-]{36}$/)
    expect(created.agentMode).toBe('ultra')
    const continued = await mock.post('/api/thread-actors', { threadId: created.threadId })
    expect(continued.threadId).toBe(created.threadId)
    expect(continued.agentMode).toBe('ultra')
  })

  it('answers the startup calls the CLI makes', async () => {
    mock = await startMock([])
    expect((await mock.post('/api/internal?getUserInfo', {})).ok).toBe(true)
    expect(await mock.post('/api/internal?loadPlugins', {})).toEqual({ ok: true, result: [] })
    expect(await mock.post('/api/internal?loadSkills', {})).toEqual({ ok: true, result: { sources: [] } })
    expect(await mock.post('/api/internal?somethingNew', {})).toEqual({ ok: true, result: {} })
    expect(await mock.post('/api/telemetry', {})).toEqual({ ok: true })
    expect((await fetch(`${mock.origin}/api/unknown`, { method: 'POST', body: '{}' })).status).toBe(404)
  })

  it('lists the threads with messages, newest first, and keeps an archived one out', async () => {
    mock = await startMock([])
    await mock.post(AMP_E2E_THREADS_PATH, { id: 'T-old', title: 'Old', tree: 'file:///work', messageCount: 2, updatedAt: 1000 })
    await mock.post(AMP_E2E_THREADS_PATH, { id: 'T-new', title: 'New', tree: 'file:///work', messageCount: 4, updatedAt: 2000 })
    await mock.post(AMP_E2E_THREADS_PATH, { id: 'T-archived', title: 'Archived', tree: 'file:///work', messageCount: 1, archived: true })
    await mock.post(AMP_E2E_THREADS_PATH, { id: 'T-empty', title: 'Empty', tree: 'file:///work', messageCount: 0 })
    const listed = await mock.post('/api/internal?listThreads', { params: { limit: 10 } })
    const threads = (listed.result as { threads: Record<string, unknown>[] }).threads
    expect(threads.map(thread => thread.id)).toEqual(['T-new', 'T-old'])
    expect(threads[0]).toMatchObject({ title: 'New', messageCount: 4, userLastInteractedAt: 2000, env: { initial: { trees: [{ uri: 'file:///work' }] } } })

    await mock.post('/api/internal?archiveThread', { params: { thread: 'T-new' } })
    const after = await mock.post('/api/internal?listThreads', { params: {} })
    expect((after.result as { threads: Record<string, unknown>[] }).threads.map(thread => thread.id)).toEqual(['T-old'])
  })

  it('answers the thread tail the CLI asks for before it connects', async () => {
    mock = await startMock([])
    const tail = await mock.post('/api/internal?getThreadTail', { params: { thread: 'T-tail' } })
    expect(tail).toMatchObject({ ok: true, result: { thread: { data: { id: 'T-tail' } }, messages: [] } })
  })
})

describe('the Amp actor', () => {
  it('opens each socket with the readiness frame and answers a ping', async () => {
    mock = await startMock([])
    const socket = await CliSocket.open(mock.origin, 'T-ping')
    socket.socket.send('ping')
    await expect.poll(() => socket.frames.filter(frame => frame === 'pong').length).toBe(2)
    socket.close()
  })

  it('answers every request it does not implement, a batch included', async () => {
    mock = await startMock([])
    const socket = await CliSocket.open(mock.origin, 'T-batch')
    socket.socket.send(JSON.stringify([
      { jsonrpc: '2.0', id: 'a', method: 'executor_skill_snapshot', params: {} },
      { jsonrpc: '2.0', id: 'b', method: 'executor_tools_register', params: {} },
    ]))
    await expect.poll(() => socket.frames.filter(frame => typeof frame === 'object').length).toBe(2)
    expect(socket.frames.slice(1)).toEqual([
      { jsonrpc: '2.0', id: 'a', result: { ok: true } },
      { jsonrpc: '2.0', id: 'b', result: { ok: true } },
    ])
  })

  it('runs a text turn, and broadcasts it to both sockets', async () => {
    mock = await startMock([{ reasoning: 'Thinking.', text: 'Hello from the mock.' }])
    const { client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s1 hello'))
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    for (const socket of [client, executor]) {
      const messages = socket.messages()
      expect(messages.map(message => message.role)).toEqual(['user', 'assistant'])
      expect(messages[1]).toMatchObject({ state: { type: 'complete' }, content: [{ type: 'thinking', thinking: 'Thinking.' }, { type: 'text', text: 'Hello from the mock.' }] })
      expect(String(messages[1]!.messageId)).toMatch(ID_PATTERN.message)
    }
    expect(mock.inferences[0]?.userText).toBe('LEAPMUXE2ESCENARIO:s1 hello')
  })

  it('leases a local tool to the executor alone, under its executor name, and continues with the result', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'call-1', name: 'shell_command', arguments: { command: 'ls' } }] },
      { text: 'Listed.' },
    ])
    const { threadID, client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s2 list'))
    await expect.poll(() => executor.notifications('tool_lease').length).toBe(1)
    expect(client.notifications('tool_lease')).toEqual([])
    const lease = executor.notifications('tool_lease')[0]!
    expect(lease).toMatchObject({ toolCallId: ampToolUseID(threadID, 'call-1'), toolName: 'async_shell_command', args: { command: 'ls' } })

    executor.request('executor_tool_result', { toolCallId: lease.toolCallId, run: { status: 'done', result: { output: 'a.ts\n', exitCode: 0 } } })
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    const messages = client.messages()
    expect(messages.map(message => message.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(messages[1]).toMatchObject({ content: [{ type: 'tool_use', id: lease.toolCallId, name: 'shell_command', input: { command: 'ls' } }] })
    expect(messages[2]).toMatchObject({ content: [{ type: 'tool_result', toolUseID: lease.toolCallId, run: { status: 'done' } }] })
    expect(mock.inferences[1]?.userText).toContain('a.ts')
    expect(executor.notifications('executor_tool_result_ack')).toEqual([{ toolCallId: lease.toolCallId }])
  })

  it('runs a subagent tool as one more inference on the server', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'task-1', name: 'Task', arguments: { description: 'Return pong', prompt: 'LEAPMUXE2ESCENARIO:s3 child: reply pong' } }] },
      { text: 'pong' },
      { text: 'The child said pong.' },
    ])
    const { client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s3 delegate'))
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(mock.inferences[1]?.userText).toBe('LEAPMUXE2ESCENARIO:s3 child: reply pong')
    expect(executor.notifications('tool_lease')).toEqual([])
    expect(client.messages()[2]).toMatchObject({ content: [{ type: 'tool_result', run: { status: 'done', result: 'pong' } }] })
  })

  it('holds a steering line for the next interruption point of the turn', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'call-1', name: 'shell_command', arguments: { command: 'sleep 5' } }] },
      { text: 'finished steered' },
    ])
    const { client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s4 sleep'))
    await expect.poll(() => executor.notifications('tool_lease').length).toBe(1)
    const steer = client.request('client_append_user_msg', userMessage('Also say steered.', { steer: true }))
    // The reply proves that the actor read the steer. A check before it passes
    // whatever the actor does, because the frame can still be on its way.
    await expect.poll(() => client.reply(steer)).toEqual({ ok: true })
    // The steer waits: the turn is still in its tool.
    expect(client.messages().map(message => message.role)).toEqual(['user', 'assistant'])
    const lease = executor.notifications('tool_lease')[0]!
    executor.request('executor_tool_result', { toolCallId: lease.toolCallId, run: { status: 'done', result: { output: '', exitCode: 0 } } })
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    // The roles are the same in either order, so the content states that the steer
    // came AFTER the tool's result.
    const messages = client.messages()
    expect(messages.map(message => message.role)).toEqual(['user', 'assistant', 'user', 'user', 'assistant'])
    expect(messages[2]).toMatchObject({ content: [{ type: 'tool_result', toolUseID: lease.toolCallId }] })
    expect(messages[3]).toMatchObject({ content: [{ type: 'text', text: 'Also say steered.' }] })
    expect(mock.inferences[1]?.userText).toBe('Also say steered.')
  })

  it('ends a turn that the CLI cancels during a delay', async () => {
    mock = await startMock([{ text: 'late', delayMs: 60_000 }])
    const first = await openThread(mock)
    first.client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s5 wait'))
    await expect.poll(() => mock!.inferences.length).toBe(1)
    first.client.request('client_cancel', {})
    await expect.poll(() => first.client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(first.client.messages().map(message => message.role)).toEqual(['user'])
  })

  it('ends a turn whose lease the CLI cancels, and runs the next turn', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'call-1', name: 'shell_command', arguments: { command: 'sleep 40' } }] },
      { text: 'next' },
    ])
    const { client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s6 sleep'))
    await expect.poll(() => executor.notifications('tool_lease').length).toBe(1)
    client.request('client_cancel', {})
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(client.messages().map(message => message.role)).toEqual(['user', 'assistant'])

    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s6 again'))
    await expect.poll(() => client.messages().length).toBe(4)
    expect(client.messages().at(-1)).toMatchObject({ content: [{ type: 'text', text: 'next' }] })
  })

  it('fails a turn that nothing scripted, and one whose step states an error', async () => {
    mock = await startMock([undefined, { error: { status: 529, message: 'Model Provider Overloaded', code: 'overloaded' } }])
    const { client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s7 one'))
    await expect.poll(() => client.notifications('error_set').length).toBe(1)
    expect(client.notifications('error_set')[0]).toMatchObject({ error: { code: 'leapmux_e2e_unscripted' } })
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')

    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s7 two'))
    await expect.poll(() => client.notifications('error_set').length).toBe(2)
    expect(client.notifications('error_set')[1]).toMatchObject({ error: { message: 'Model Provider Overloaded', code: 'overloaded' } })
  })

  it('records the workspace tree and a title for the thread picker', async () => {
    mock = await startMock([{ text: 'ok' }])
    const { threadID, client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s8 name this thread'))
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    const views = await (await fetch(`${mock.origin}${AMP_E2E_THREADS_PATH}`)).json() as Record<string, unknown>[]
    expect(views.find(view => view.id === threadID)).toMatchObject({ agentMode: 'high', tree: 'file:///work', title: 'LEAPMUXE2ESCENARIO:s8 name this thread', messageCount: 2 })
  })

  it('refuses an upgrade that states no thread, and one outside the actor gateway', async () => {
    mock = await startMock([])
    const origin = mock.origin.replace('http:', 'ws:')
    for (const url of [`${origin}/actors/gateway/threadActor/websocket/`, `${origin}/elsewhere/?rvt-key=T-1`]) {
      const socket = new WebSocket(url, ['rivet'])
      await new Promise<void>(resolve => socket.addEventListener('error', () => resolve(), { once: true }))
    }
    const views = await (await fetch(`${mock.origin}${AMP_E2E_THREADS_PATH}`)).json() as unknown[]
    expect(views).toEqual([])
  })

  // The last answer is an interruption point too: a steering line that arrived
  // while it ran continues the turn instead of waiting for a turn that never comes.
  it('continues the turn with a steering line that arrives during its last answer', async () => {
    const held = heldAnswer()
    mock = await startMock([held.promise, { text: 'steered answer' }])
    const { client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s9 start'))
    await expect.poll(() => mock!.inferences.length).toBe(1)
    const steer = client.request('client_append_user_msg', userMessage('Also say steered.'))
    await expect.poll(() => client.reply(steer)).toEqual({ ok: true })

    held.settle({ text: 'first answer' })
    await expect.poll(() => client.messages().length).toBe(4)
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(client.messages().map(message => message.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(mock.inferences[1]?.userText).toBe('Also say steered.')
  })

  // A failed turn reaches no interruption point, so its steering line ends with it.
  // A line that outlived the turn would reach the NEXT turn and ask the script for
  // an inference that no test wrote.
  it('drops the steering line of a turn that fails', async () => {
    const held = heldAnswer()
    mock = await startMock([held.promise, { text: 'second turn' }])
    const { client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s10 first'))
    await expect.poll(() => mock!.inferences.length).toBe(1)
    const steer = client.request('client_append_user_msg', userMessage('A stale steer.'))
    await expect.poll(() => client.reply(steer)).toEqual({ ok: true })
    held.settle(undefined)
    await expect.poll(() => client.notifications('error_set').length).toBe(1)

    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s10 second'))
    await expect.poll(() => client.messages().some(message => JSON.stringify(message.content).includes('second turn'))).toBe(true)
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(mock.inferences).toHaveLength(2)
    expect(client.notifications('error_set')).toHaveLength(1)
    expect(client.messages().map(message => message.role)).toEqual(['user', 'user', 'assistant'])
  })

  it('fails a local tool call when no executor is connected, and continues with the error', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'call-1', name: 'shell_command', arguments: { command: 'ls' } }] },
      { text: 'No executor.' },
    ])
    const created = await mock.post('/api/thread-actors', {})
    const client = await CliSocket.open(mock.origin, String(created.threadId))
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s11 list'))
    await expect.poll(() => client.messages().length).toBe(4)
    expect(client.notifications('tool_lease')).toEqual([])
    expect(client.messages()[2]).toMatchObject({
      content: [{ type: 'tool_result', run: { status: 'error', error: { message: 'No executor is connected to the thread.' } } }],
    })
  })

  it('reports a subagent that nothing scripted, or whose answer is an error, as a failed run', async () => {
    mock = await startMock([
      { toolCalls: [{ id: 'task-1', name: 'Task', arguments: { prompt: 'LEAPMUXE2ESCENARIO:s12 child one' } }] },
      undefined,
      { toolCalls: [{ id: 'task-2', name: 'Task', arguments: { prompt: 'LEAPMUXE2ESCENARIO:s12 child two' } }] },
      { error: { status: 500, message: 'The child failed.' } },
      { text: 'Both children failed.' },
    ])
    const { client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s12 delegate'))
    await expect.poll(() => client.messages().length).toBe(6)
    expect(client.messages()[2]).toMatchObject({
      content: [{ type: 'tool_result', run: { status: 'error', error: { message: 'The mock model has no scripted answer for this subagent.' } } }],
    })
    expect(client.messages()[4]).toMatchObject({ content: [{ type: 'tool_result', run: { status: 'error', error: { message: 'The child failed.' } } }] })
    expect(client.notifications('error_set')).toEqual([])
  })

  // Each subagent tool states its request under a key of its own.
  it('builds the prompt of each subagent tool from its own keys', async () => {
    mock = await startMock([
      {
        toolCalls: [
          { id: 'oracle-1', name: 'oracle', arguments: { task: 'Review the plan.', context: 'The plan is short.' } },
          { id: 'librarian-1', name: 'librarian', arguments: { query: 'Find the parser.' } },
        ],
      },
      { text: 'Reviewed.' },
      { text: 'Found.' },
      { text: 'Done.' },
    ])
    const { client } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s13 consult'))
    await expect.poll(() => client.notifications('agent_state').at(-1)?.state).toBe('idle')
    expect(mock.inferences.slice(1, 3).map(inference => inference.userText)).toEqual(['Review the plan.\n\nThe plan is short.', 'Find the parser.'])
    expect(client.messages()[2]).toMatchObject({
      content: [{ type: 'tool_result', run: { status: 'done', result: 'Reviewed.' } }, { type: 'tool_result', run: { status: 'done', result: 'Found.' } }],
    })
  })

  it('reports the working state to a socket that resumes during a turn', async () => {
    const held = heldAnswer()
    mock = await startMock([held.promise])
    const { threadID, client } = await openThread(mock)
    // The actor sends the state batch before the reply, so the reply states that both arrived.
    await expect.poll(() => client.reply('client-0')).toEqual({ replayMessageCount: 0, storedEventCount: 0, replayThroughSeq: 0 })
    const idleBatch = client.frames.find(Array.isArray) as Record<string, unknown>[] | undefined
    expect(idleBatch?.[0]).toEqual({ type: 'agent_state', state: 'idle', agentMode: 'high' })

    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s14 hold'))
    await expect.poll(() => mock!.inferences.length).toBe(1)
    const late = await CliSocket.open(mock.origin, threadID)
    const resume = late.request('client_resume', {})
    await expect.poll(() => late.reply(resume)).toEqual({ replayMessageCount: 0, storedEventCount: 0, replayThroughSeq: 1 })
    expect((late.frames.find(Array.isArray) as Record<string, unknown>[])[0]).toEqual({ type: 'agent_state', state: 'working', agentMode: 'high' })
    held.settle({ text: 'released' })
  })

  it('acknowledges a tool result that no lease waits for, and changes nothing else', async () => {
    mock = await startMock([])
    const { client, executor } = await openThread(mock)
    const result = executor.request('executor_tool_result', { toolCallId: 'TU-unknown', run: { status: 'done' } })
    await expect.poll(() => executor.reply(result)).toEqual({ ok: true })
    await expect.poll(() => client.notifications('executor_tool_result_ack')).toEqual([{ toolCallId: 'TU-unknown' }])
    expect(client.messages()).toEqual([])
    expect(client.notifications('agent_state')).toEqual([])
  })

  it('closes each socket and ends a turn that holds a lease when the surface closes', async () => {
    mock = await startMock([{ toolCalls: [{ id: 'call-1', name: 'shell_command', arguments: { command: 'sleep 40' } }] }])
    const { client, executor } = await openThread(mock)
    client.request('client_append_user_msg', userMessage('LEAPMUXE2ESCENARIO:s15 sleep'))
    await expect.poll(() => executor.notifications('tool_lease').length).toBe(1)
    const closed = Promise.all([client, executor].map(socket => new Promise<number>((resolve) => {
      socket.socket.addEventListener('close', event => resolve(event.code), { once: true })
    })))
    const current = mock
    mock = undefined
    await current.close()
    expect(await closed).toEqual([1000, 1000])
    // The cancelled turn adds no message after the close.
    expect(client.messages().map(message => message.role)).toEqual(['user', 'assistant'])
  })
})

describe('the Amp thread list', () => {
  it('pages the list by offset and limit, newest first', async () => {
    mock = await startMock([])
    for (const [id, updatedAt] of [['T-1', 1000], ['T-2', 2000], ['T-3', 3000]] as const)
      await mock.post(AMP_E2E_THREADS_PATH, { id, title: id, tree: '', messageCount: 1, updatedAt })
    const page = await mock.post('/api/internal?listThreads', { params: { offset: 1, limit: 1 } })
    expect((page.result as { threads: Record<string, unknown>[] }).threads.map(thread => thread.id)).toEqual(['T-2'])
    // A thread with no tree states no tree rather than an empty one.
    expect((page.result as { threads: Record<string, unknown>[] }).threads[0]).toMatchObject({ title: 'T-2', env: { initial: { trees: [] } } })
  })

  it('archives a thread that the request states under either key, and ignores an unknown one', async () => {
    mock = await startMock([])
    for (const id of ['T-a', 'T-b', 'T-c'])
      await mock.post(AMP_E2E_THREADS_PATH, { id, title: id, tree: '', messageCount: 1 })
    expect(await mock.post('/api/internal?archiveThread', { params: { threadID: 'T-a' } })).toEqual({ ok: true, result: {} })
    expect(await mock.post('/api/internal?archiveThread', { params: { thread: 'T-b' } })).toEqual({ ok: true, result: {} })
    expect(await mock.post('/api/internal?archiveThread', { params: { thread: 'T-unknown' } })).toEqual({ ok: true, result: {} })
    const views = await (await fetch(`${mock.origin}${AMP_E2E_THREADS_PATH}`)).json() as Record<string, unknown>[]
    expect(views.map(view => [view.id, view.archived])).toEqual([['T-a', true], ['T-b', true], ['T-c', false]])
  })

  it('answers a seeded thread with its view', async () => {
    mock = await startMock([])
    expect(await mock.post(AMP_E2E_THREADS_PATH, { id: 'T-seed', title: 'Seed', tree: 'file:///w', messageCount: 3 }))
      .toEqual({ id: 'T-seed', agentMode: 'medium', tree: 'file:///w', title: 'Seed', messageCount: 3, archived: false })
  })
})
