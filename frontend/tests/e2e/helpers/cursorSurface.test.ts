import type { AddressInfo } from 'node:net'
import { Buffer } from 'node:buffer'
import { createServer } from 'node:http'
import { afterEach, describe, expect, it } from 'vitest'
import {
  answerCursorStartup,
  CURSOR_GENERATE_IMAGE_TOOL,
  CURSOR_MCP_TOOL,
  CURSOR_MOCK_MODELS,
  CURSOR_RUN_PATH,
  CURSOR_TASK_TOOL,
  cursorToolCallsFrom,
  isCursorPath,
  serveCursorRun,
} from './cursorSurface'
import {
  connectFrame,
  descend,
  encodeLengthDelimited,
  encodeStringField,
  readLengthDelimitedFields,
  takeConnectFrames,
} from './cursorWire'

/** `AgentServerMessage.interaction_update`, and the updates inside it. */
const FIELD_INTERACTION_UPDATE = 1
const FIELD_TEXT_DELTA = 1
const FIELD_TOOL_CALL_STARTED = 2
const FIELD_TOOL_CALL_COMPLETED = 3
const FIELD_THINKING_DELTA = 4
const FIELD_TURN_ENDED = 14
/** `AgentServerMessage.kv_server_message`, which carries the transcript blobs. */
const FIELD_KV_SERVER_MESSAGE = 4

/** The client message that OPENS a turn; mirrors the encoder in `./cursorWire.test.ts`. */
function clientMessageWithPrompt(text: string): Uint8Array {
  return encodeLengthDelimited(
    1, // AgentClientMessage.run_request
    encodeLengthDelimited(
      2, // AgentRunRequest.action
      encodeLengthDelimited(
        1, // ConversationAction.user_message_action
        encodeLengthDelimited(
          1, // UserMessageAction.user_message
          encodeStringField(1, text), // UserMessage.text
        ),
      ),
    ),
  )
}

/**
 * Which update each server frame carries, in the order the stream sent them.
 *
 * A name rather than the bytes, so an assertion states the SEQUENCE a real turn
 * uses -- open the row, close it, write the transcript, speak, end -- which is
 * the part a reordering would break.
 */
function updateKinds(body: Buffer): string[] {
  const { frames } = takeConnectFrames(new Uint8Array(body))
  const kinds: string[] = []
  for (const frame of frames) {
    const fields = readLengthDelimitedFields(frame)
    if (fields.has(FIELD_KV_SERVER_MESSAGE)) {
      kinds.push('setBlob')
      continue
    }
    const update = fields.get(FIELD_INTERACTION_UPDATE)?.[0]
    if (!update) {
      // The end-of-stream frame carries trailers, not an update.
      kinds.push('endOfStream')
      continue
    }
    const inner = readLengthDelimitedFields(update)
    if (inner.has(FIELD_TOOL_CALL_STARTED))
      kinds.push('toolStarted')
    else if (inner.has(FIELD_TOOL_CALL_COMPLETED))
      kinds.push('toolCompleted')
    else if (inner.has(FIELD_THINKING_DELTA))
      kinds.push('thinkingDelta')
    else if (inner.has(FIELD_TEXT_DELTA))
      kinds.push('textDelta')
    else if (inner.has(FIELD_TURN_ENDED))
      kinds.push('turnEnded')
    else
      kinds.push('unknown')
  }
  return kinds
}

const close: Array<() => Promise<void>> = []

afterEach(async () => {
  for (const stop of close.splice(0))
    await stop()
})

/** A server that answers `/agent.v1.AgentService/Run` with one scripted turn. */
async function runStreamBody(answer: Parameters<typeof serveCursorRun>[2]['answer']): Promise<Buffer> {
  const server = createServer((request, response) => {
    void serveCursorRun(request, response, { answer })
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
  close.push(() => new Promise<void>((resolve) => {
    server.closeAllConnections()
    server.close(() => resolve())
  }))
  const { port } = server.address() as AddressInfo
  const response = await fetch(`http://127.0.0.1:${port}${CURSOR_RUN_PATH}`, {
    method: 'POST',
    headers: { 'content-type': 'application/connect+proto' },
    body: Buffer.from(connectFrame(clientMessageWithPrompt('Say the mock word.'))),
  })
  return Buffer.from(await response.arrayBuffer())
}

async function runStream(answer: Parameters<typeof serveCursorRun>[2]['answer']): Promise<string> {
  return updateKinds(await runStreamBody(answer)).join(',')
}

describe('isCursorPath', () => {
  it('claims Cursor\'s own services, the Run stream and the trace endpoint', () => {
    expect(isCursorPath('/aiserver.v1.AiService/AvailableModels')).toBe(true)
    expect(isCursorPath(CURSOR_RUN_PATH)).toBe(true)
    expect(isCursorPath('/v1/traces')).toBe(true)
  })

  it('leaves a model API path to the model server', () => {
    // These three are the mock endpoint's own routes. Claiming one would answer
    // a model call with an empty protobuf body, and the agent would read an
    // empty turn rather than the scripted one.
    expect(isCursorPath('/v1/chat/completions')).toBe(false)
    expect(isCursorPath('/v1/responses')).toBe(false)
    expect(isCursorPath('/v1/messages')).toBe(false)
  })

  it('does not claim a path that merely contains a Cursor service name', () => {
    // `startsWith`, not `includes`. A proxy prefix must not turn a model call
    // into a Cursor one.
    expect(isCursorPath('/proxy/aiserver.v1.AiService/AvailableModels')).toBe(false)
  })
})

describe('CURSOR_MOCK_MODELS', () => {
  it('keeps the bracketed-id shape LeapMux parses', () => {
    for (const model of CURSOR_MOCK_MODELS) {
      expect(model.variants.length, `model ${model.name}`).toBeGreaterThan(0)
      for (const variant of model.variants)
        expect(variant.id, `model ${model.name}`).toMatch(/^[\w.-]+\[[^\]]*\]$/)
    }
  })

  it('spells the effort level both ways the real catalogue does', () => {
    // Cursor uses `effort=` on some models and `reasoning_effort=` on others.
    // A catalogue that carried one spelling would leave the other path of the
    // parser untested by every Cursor specification.
    const ids = CURSOR_MOCK_MODELS.flatMap(model => model.variants.map(variant => variant.id))
    expect(ids.some(id => /[[,]effort=/.test(id))).toBe(true)
    expect(ids.some(id => id.includes('reasoning_effort='))).toBe(true)
  })

  it('marks exactly one variant as the default', () => {
    const defaults = CURSOR_MOCK_MODELS.flatMap(m => m.variants).filter(v => v.isDefault)
    expect(defaults).toHaveLength(1)
    expect(defaults[0]?.id).toBe('default[]')
  })
})

describe('cursorToolCallsFrom', () => {
  it('answers an empty list for an absent or empty tool-call list', () => {
    expect(cursorToolCallsFrom(undefined)).toEqual([])
    expect(cursorToolCallsFrom([])).toEqual([])
  })

  it('keeps a native Task call', () => {
    const calls = cursorToolCallsFrom([
      { id: 'b', name: CURSOR_TASK_TOOL, arguments: { description: 'Probe', prompt: 'Go.', report: 'PONG' } },
    ])
    expect(calls).toEqual([{ kind: 'task', call: { callID: 'b', description: 'Probe', prompt: 'Go.' }, report: 'PONG' }])
  })

  it('refuses an unknown tool instead of silently dropping a scripted call', () => {
    expect(() => cursorToolCallsFrom([{ id: 'a', name: 'Bash', arguments: { command: 'echo hi' } }]))
      .toThrow('no native encoder for Bash')
  })

  it('substitutes an empty string for each absent argument', () => {
    // The encoder writes every field, so an undefined would reach the wire as
    // the text "undefined" rather than as an absent value.
    expect(cursorToolCallsFrom([{ id: 'b', name: CURSOR_TASK_TOOL }]))
      .toEqual([{ kind: 'task', call: { callID: 'b', description: '', prompt: '' }, report: '' }])
  })

  it('keeps Todo calls in order and gives each row a stable native id', () => {
    expect(cursorToolCallsFrom([
      { id: 'todo-1', name: 'updateTodos', arguments: { todos: [
        { content: 'Inspect', status: 'TODO_STATUS_COMPLETED' },
        { content: 'Report', status: 'TODO_STATUS_IN_PROGRESS' },
      ] } },
      { id: 'task-2', name: CURSOR_TASK_TOOL, arguments: { description: 'Review', prompt: 'Check.' } },
    ])).toEqual([
      { kind: 'todo', call: { callID: 'todo-1', merge: false, todos: [
        { id: '1', content: 'Inspect', status: 'completed' },
        { id: '2', content: 'Report', status: 'in_progress' },
      ] } },
      { kind: 'task', call: { callID: 'task-2', description: 'Review', prompt: 'Check.' }, report: '' },
    ])
  })

  it('refuses malformed Todo rows and unknown status words', () => {
    expect(() => cursorToolCallsFrom([{ id: 'x', name: 'updateTodos', arguments: {} }])).toThrow('todos list')
    expect(() => cursorToolCallsFrom([{ id: 'x', name: 'updateTodos', arguments: { todos: [{ status: 'TODO_STATUS_PENDING' }] } }])).toThrow('needs content')
    expect(() => cursorToolCallsFrom([{ id: 'x', name: 'updateTodos', arguments: { todos: [{ content: 'Inspect', status: 'wrong' }] } }])).toThrow('unsupported status')
  })

  it('keeps a native GenerateImage call and its image data', () => {
    expect(cursorToolCallsFrom([{
      id: 'image-1',
      name: CURSOR_GENERATE_IMAGE_TOOL,
      arguments: { description: 'A square', filePath: '/work/square.png', imageData: 'cG5n' },
    }])).toEqual([{
      kind: 'generateImage',
      call: { callID: 'image-1', description: 'A square', filePath: '/work/square.png', imageData: 'cG5n' },
    }])
  })

  it('refuses an incomplete GenerateImage result', () => {
    expect(() => cursorToolCallsFrom([{
      id: 'image-1',
      name: CURSOR_GENERATE_IMAGE_TOOL,
      arguments: { description: 'A square', filePath: '/work/square.png' },
    }])).toThrow('needs a description, file path, and image data')
  })

  it('reads native question, plan, and web-fetch queries', () => {
    expect(cursorToolCallsFrom([
      { id: 'q', name: 'askQuestion', arguments: { title: 'Color', questions: [
        { id: 'question-1', prompt: 'Which color?', options: [{ id: 'blue', label: 'Blue' }], allowMultiple: false },
      ] } },
      { id: 'p', name: 'createPlan', arguments: { name: 'Review', overview: 'No edits', plan: '# Plan' } },
      { id: 'f', name: 'webFetch', arguments: { url: 'https://example.invalid/probe' } },
    ])).toEqual([
      { kind: 'question', callID: 'q', title: 'Color', questions: [
        { id: 'question-1', prompt: 'Which color?', options: [{ id: 'blue', label: 'Blue' }], allowMultiple: false },
      ] },
      { kind: 'plan', callID: 'p', name: 'Review', overview: 'No edits', plan: '# Plan' },
      { kind: 'webFetch', callID: 'f', url: 'https://example.invalid/probe' },
    ])
  })

  it('refuses malformed native interaction queries', () => {
    expect(() => cursorToolCallsFrom([{ id: 'q', name: 'askQuestion', arguments: { title: 'Color', questions: [] } }]))
      .toThrow('needs a title and questions')
    expect(() => cursorToolCallsFrom([{ id: 'p', name: 'createPlan', arguments: { name: 'Review' } }]))
      .toThrow('needs a name, overview, and plan')
    expect(() => cursorToolCallsFrom([{ id: 'f', name: 'webFetch', arguments: { url: 'file:///work/secret' } }]))
      .toThrow('needs an HTTP URL')
  })

  it('keeps a native local MCP call and its structured input', () => {
    expect(cursorToolCallsFrom([{
      id: 'mcp-1',
      name: CURSOR_MCP_TOOL,
      arguments: { server: 'form_probe', tool: 'echo', input: { count: 0, enabled: false } },
    }])).toEqual([{
      kind: 'mcp',
      call: { callID: 'mcp-1', server: 'form_probe', tool: 'echo', input: { count: 0, enabled: false } },
    }])
  })

  it('refuses a local MCP call with no server or object input', () => {
    expect(() => cursorToolCallsFrom([{ id: 'mcp-1', name: CURSOR_MCP_TOOL, arguments: { server: '', tool: 'ask', input: {} } }]))
      .toThrow('needs a server, tool, and object input')
    expect(() => cursorToolCallsFrom([{ id: 'mcp-1', name: CURSOR_MCP_TOOL, arguments: { server: 'form_probe', tool: 'ask', input: [] } }]))
      .toThrow('needs a server, tool, and object input')
  })
})

describe('answerCursorStartup', () => {
  it('answers the model catalogue with an uncompressed protobuf body', async () => {
    const server = createServer((request, response) => {
      answerCursorStartup(request, response, new URL(request.url!, 'http://mock.invalid').pathname)
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    close.push(() => new Promise<void>((resolve) => {
      server.closeAllConnections()
      server.close(() => resolve())
    }))
    const { port } = server.address() as AddressInfo

    const models = await fetch(`http://127.0.0.1:${port}/aiserver.v1.AiService/AvailableModels`, { method: 'POST' })
    const body = Buffer.from(await models.arrayBuffer())
    expect(models.headers.get('content-type')).toBe('application/proto')
    // No `content-encoding`, so the body must not be gzip. A recording replayed
    // with gzip bytes and no header made the client read `0x1f` as field 3,
    // wire type 7 -- which is not a wire type -- and substitute an empty list.
    expect(models.headers.get('content-encoding')).toBeNull()
    expect(body[0]).not.toBe(0x1F)
    expect(body.byteLength).toBeGreaterThan(0)
    // The first model's name survives the encoding, which is what the picker reads.
    expect(body.includes(Buffer.from('mock-sonnet'))).toBe(true)

    // A service with no catalogue takes an all-defaults answer, in the encoding
    // the request asked for.
    const json = await fetch(`http://127.0.0.1:${port}/aiserver.v1.DashboardService/GetUserPrivacyMode`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
    })
    expect(json.headers.get('content-type')).toBe('application/json')
    expect(await json.text()).toBe('{}')

    const proto = await fetch(`http://127.0.0.1:${port}/aiserver.v1.DashboardService/GetUserPrivacyMode`, { method: 'POST' })
    expect(proto.headers.get('content-type')).toBe('application/proto')
    expect((await proto.arrayBuffer()).byteLength).toBe(0)
  })
})

describe('serveCursorRun', () => {
  it('writes a native transcript blob for a plain text turn so session/load can reopen it', async () => {
    const body = await runStreamBody(async () => ({ text: 'Plain answer survives the native session.' }))
    const records = takeConnectFrames(new Uint8Array(body)).frames.flatMap((frame) => {
      const data = descend(frame, [FIELD_KV_SERVER_MESSAGE, 3, 2])
      return data === undefined ? [] : [JSON.parse(new TextDecoder().decode(data))]
    })
    expect(records).toEqual(expect.arrayContaining([
      expect.objectContaining({ role: 'user', content: [{ type: 'text', text: 'Say the mock word.' }] }),
      expect.objectContaining({ role: 'assistant', content: [{ type: 'text', text: 'Plain answer survives the native session.' }] }),
    ]))
  })

  it('sends the text and ends the turn when the answer holds no task', async () => {
    expect(await runStream(async () => ({ text: 'Delegated and done.' })))
      .toBe('setBlob,setBlob,textDelta,turnEnded,endOfStream')
  })

  it('opens the row, closes it, writes the transcript, then speaks', async () => {
    // The order is what a real turn uses. The two blobs are the only place the
    // child's report survives -- the agent reads them out of Cursor's own store.
    expect(await runStream(async () => ({
      text: 'Delegated and done.',
      toolCalls: [{ kind: 'task', call: { callID: 'task-1', description: 'Probe', prompt: 'Go.' }, report: 'PONG' }],
    }))).toBe('setBlob,toolStarted,toolCompleted,setBlob,setBlob,setBlob,textDelta,turnEnded,endOfStream')
  })

  it('writes a Todo snapshot before its answer', async () => {
    expect(await runStream(async () => ({
      text: 'The list is ready.',
      toolCalls: [{ kind: 'todo', call: { callID: 'todo-1', merge: false, todos: [{ id: '1', content: 'Inspect', status: 'pending' }] } }],
    }))).toBe('setBlob,toolStarted,toolCompleted,setBlob,textDelta,turnEnded,endOfStream')
  })

  it('ends the turn without a delta when the answer holds no text', async () => {
    // An empty string is not a delta either: a zero-length text update reaches
    // the transcript as an empty assistant message rather than as no message.
    expect(await runStream(async () => ({ text: '' }))).toBe('setBlob,turnEnded,endOfStream')
    expect(await runStream(async () => undefined)).toBe('setBlob,turnEnded,endOfStream')
  })

  it('reads the prompt out of the frame that opens the turn', async () => {
    let seen: string | undefined
    await runStream(async (prompt) => {
      seen = prompt
      return { text: 'ok' }
    })
    expect(seen).toBe('Say the mock word.')
  })

  it('answers the turn once, however many frames follow', async () => {
    // A heartbeat and a tool result travel on the same stream. Only the frame
    // that yields a prompt opens a turn, and a second answer would write a
    // second turn into one stream.
    const calls: string[] = []
    const server = createServer((request, response) => {
      void serveCursorRun(request, response, {
        answer: async (prompt) => {
          calls.push(prompt)
          return { text: 'ok' }
        },
      })
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    close.push(() => new Promise<void>((resolve) => {
      server.closeAllConnections()
      server.close(() => resolve())
    }))
    const { port } = server.address() as AddressInfo
    const body = Buffer.concat([
      Buffer.from(connectFrame(clientMessageWithPrompt('First.'))),
      Buffer.from(connectFrame(clientMessageWithPrompt('Second.'))),
    ])
    const response = await fetch(`http://127.0.0.1:${port}${CURSOR_RUN_PATH}`, {
      method: 'POST',
      headers: { 'content-type': 'application/connect+proto' },
      body,
    })
    expect(updateKinds(Buffer.from(await response.arrayBuffer())).join(','))
      .toBe('setBlob,setBlob,textDelta,turnEnded,endOfStream')
    expect(calls).toEqual(['First.'])
  })

  it('passes the complete Run frame to the answer handler', async () => {
    let captured: Uint8Array | undefined
    await runStream(async (_prompt, requestFrame) => {
      captured = requestFrame
      return { text: 'ok' }
    })
    expect(captured).toEqual(clientMessageWithPrompt('Say the mock word.'))
  })

  it('streams native thinking before the answer', async () => {
    const kinds = await runStream(async () => ({ reasoning: 'I checked.', text: 'Done.' }))
    expect(kinds).toBe('setBlob,setBlob,thinkingDelta,textDelta,turnEnded,endOfStream')
  })

  it('closes the stream cleanly when the client opens no turn', async () => {
    // A transport fault here reads in the agent as a broken endpoint; an empty
    // answer reads as a turn that said nothing, which is what happened.
    const server = createServer((request, response) => {
      void serveCursorRun(request, response, { answer: async () => ({ text: 'never' }) })
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    close.push(() => new Promise<void>((resolve) => {
      server.closeAllConnections()
      server.close(() => resolve())
    }))
    const { port } = server.address() as AddressInfo
    const response = await fetch(`http://127.0.0.1:${port}${CURSOR_RUN_PATH}`, {
      method: 'POST',
      headers: { 'content-type': 'application/connect+proto' },
      body: Buffer.from(connectFrame(encodeLengthDelimited(7, new Uint8Array(0)))),
    })
    expect(response.ok).toBe(true)
    expect(updateKinds(Buffer.from(await response.arrayBuffer())).join(',')).toBe('endOfStream')
  })

  it('writes the report into the completed update, where the row reads it', async () => {
    const server = createServer((request, response) => {
      void serveCursorRun(request, response, {
        answer: async () => ({
          toolCalls: [{ kind: 'task', call: { callID: 'task-1', description: 'Probe', prompt: 'Go.' }, report: 'PONG_FROM_CHILD' }],
        }),
      })
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    close.push(() => new Promise<void>((resolve) => {
      server.closeAllConnections()
      server.close(() => resolve())
    }))
    const { port } = server.address() as AddressInfo
    const response = await fetch(`http://127.0.0.1:${port}${CURSOR_RUN_PATH}`, {
      method: 'POST',
      headers: { 'content-type': 'application/connect+proto' },
      body: Buffer.from(connectFrame(clientMessageWithPrompt('Say the mock word.'))),
    })
    const raw = Buffer.from(await response.arrayBuffer())
    const { frames } = takeConnectFrames(new Uint8Array(raw))
    const completed = frames.find(frame =>
      descend(frame, [FIELD_INTERACTION_UPDATE, FIELD_TOOL_CALL_COMPLETED]) !== undefined)
    expect(completed, 'a completed update reached the stream').toBeDefined()
    expect(Buffer.from(completed!).includes(Buffer.from('PONG_FROM_CHILD'))).toBe(true)
    // The blobs carry it too, and they are the half that survives the turn.
    expect(raw.includes(Buffer.from('PONG_FROM_CHILD'))).toBe(true)
  })
})
