import type { AddressInfo } from 'node:net'
import type { MockModelScriptHost, ModelRequestContext, SelectedModelAnswer } from './mockModelRequest'
import type { MockModelStep } from './mockModelScript'
import { Buffer } from 'node:buffer'
import { createServer } from 'node:http'
import { gzipSync } from 'node:zlib'
import { afterEach, describe, expect, it } from 'vitest'
import { cursorSubagentReplyFixture, cursorSubagentSuccessFixture } from './cursorSubagentFixtures'
import {
  answerCursorStartup,
  createCursorSurface,
  CURSOR_GENERATE_IMAGE_TOOL,
  CURSOR_MCP_TOOL,
  CURSOR_MOCK_MODELS,
  CURSOR_REQUEST_CONTEXT_TOOL,
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
  encodeVarint,
  readLengthDelimitedFields,
  takeConnectFrames,
} from './cursorWire'
import { mockScenarioPrompt } from './mockModelScenario'
import { createModelStream } from './modelStream'

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
 * Identify each server update in the native stream order.
 * Assertions preserve the order of row events, transcript records, and turn completion.
 */
function updateKinds(body: Buffer): string[] {
  const { frames } = takeConnectFrames(new Uint8Array(body))
  const kinds: string[] = []
  for (const { flags, payload: frame } of frames) {
    if ((flags & 2) !== 0) {
      kinds.push('endOfStream')
      continue
    }
    const fields = readLengthDelimitedFields(frame)
    if (fields.has(FIELD_KV_SERVER_MESSAGE)) {
      kinds.push('setBlob')
      continue
    }
    const update = fields.get(FIELD_INTERACTION_UPDATE)?.[0]
    if (!update) {
      kinds.push(fields.has(2) ? 'execRequest' : 'unknown')
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
async function runStreamBody(
  answer: Parameters<typeof serveCursorRun>[2]['answer'],
  receipts?: { prompt: string, text: string }[],
  reply?: Uint8Array,
  opening?: { payload: Uint8Array, flags: number, encoding?: string, errors?: string[] },
): Promise<Buffer> {
  const server = createServer((request, response) => {
    const handlers = {
      answer,
      completed: (prompt: string, _frame: Uint8Array, text: string) => receipts?.push({ prompt, text }),
    }
    void serveCursorRun(request, response, handlers).catch((error: unknown) => {
      opening?.errors?.push(error instanceof Error ? error.message : String(error))
      response.destroy(error instanceof Error ? error : new Error(String(error)))
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
    headers: { 'content-type': 'application/connect+proto', ...(opening?.encoding === undefined ? {} : { 'connect-content-encoding': opening.encoding }) },
    body: Buffer.concat([
      Buffer.from(connectFrame(opening?.payload ?? clientMessageWithPrompt('Say the mock word.'), opening?.flags ?? 0)),
      ...(reply ? [Buffer.from(connectFrame(reply))] : []),
    ]),
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
  it('keeps actual native child execution separate from Task arguments and simulated child text', () => {
    const call = { id: 'native-child', name: CURSOR_TASK_TOOL, arguments: { description: 'Read a native file.', prompt: 'Read the actual file.' }, nativeExecution: { modelId: 'default' } }
    const [task] = cursorToolCallsFrom([call])
    expect(task).toMatchObject({ nativeExecution: { modelId: 'default' }, call: { callID: call.id, prompt: call.arguments.prompt } })
    expect(task).toHaveProperty('call', { callID: call.id, description: call.arguments.description, prompt: call.arguments.prompt })
    expect(() => cursorToolCallsFrom([{ ...call, name: 'read' }])).toThrow('Only a native Cursor task')
    expect(() => cursorToolCallsFrom([{ ...call, taskProgress: 'Invented child progress.' }])).toThrow('cannot use scripted child progress')
  })
  it('keeps task progress outside Task arguments and refuses it on another tool', () => {
    const tools = cursorToolCallsFrom([{ id: 'task-live', name: CURSOR_TASK_TOOL, arguments: { prompt: 'Read.' }, taskProgress: 'Native child text.', completionGate: 'native-finish' }])
    expect(tools[0]).toMatchObject({ kind: 'task', call: { callID: 'task-live', prompt: 'Read.' }, taskProgress: 'Native child text.', completionGate: 'native-finish' })
    expect(tools[0]).toHaveProperty('call', { callID: 'task-live', description: '', prompt: 'Read.' })
    expect(() => cursorToolCallsFrom([{ id: 'shell-live', name: 'shell', arguments: { command: 'pwd' }, taskProgress: 'Incorrect native child text.' }])).toThrow('Only a native Cursor task')
  })

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

describe('cursorToolCallsFrom request context', () => {
  it('keeps a request context query and its call identity', () => {
    expect(cursorToolCallsFrom([{ id: 'context-1', name: CURSOR_REQUEST_CONTEXT_TOOL, arguments: {} }]))
      .toEqual([{ kind: 'requestContext', callID: 'context-1' }])
  })

  it('refuses a request context call that carries provider-service metadata', () => {
    expect(() => cursorToolCallsFrom([{ id: 'context-1', name: CURSOR_REQUEST_CONTEXT_TOOL, arguments: {}, completionGate: 'gate' }]))
      .toThrow('Only a native Cursor task supports provider-service tool metadata')
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
  it('completes actual native child execution from its client-supplied identity and report', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const run = readLengthDelimitedFields(clientMessageWithPrompt('Say the mock word.')).get(1)![0]!
    const parent = encodeLengthDelimited(1, Buffer.concat([run, encodeStringField(5, 'actual-native-parent')]))
    const reply = cursorSubagentReplyFixture(301, cursorSubagentSuccessFixture({ agentID: 'actual-native-child', finalMessage: 'ACTUAL_NATIVE_CHILD_REPORT', toolCallCount: 1 }), 'native-child-call')
    const body = await runStreamBody(async () => ({ text: 'The native parent finished.', toolCalls: [{ kind: 'task', call: { callID: 'native-child-call', description: 'Read the actual file.', prompt: 'Execute the native child.' }, report: 'THIS_SCRIPTED_REPORT_MUST_NOT_APPEAR', nativeExecution: { modelId: 'default' } }] }), receipts, reply, { payload: parent, flags: 0 })
    expect(body.includes(Buffer.from('actual-native-child'))).toBe(true)
    expect(body.includes(Buffer.from('ACTUAL_NATIVE_CHILD_REPORT'))).toBe(true)
    expect(body.includes(Buffer.from('THIS_SCRIPTED_REPORT_MUST_NOT_APPEAR'))).toBe(false)
    expect(updateKinds(body).filter(kind => kind === 'toolCompleted')).toHaveLength(1)
    expect(receipts).toEqual([{ prompt: 'Say the mock word.', text: 'The native parent finished.\nACTUAL_NATIVE_CHILD_REPORT' }])
  })

  it('does not complete a native child when no actual client reply arrives', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const run = readLengthDelimitedFields(clientMessageWithPrompt('Say the mock word.')).get(1)![0]!
    const parent = encodeLengthDelimited(1, Buffer.concat([run, encodeStringField(5, 'actual-native-parent')]))
    const body = await runStreamBody(async () => ({ toolCalls: [{ kind: 'task', call: { callID: 'native-child-call', description: 'Read.', prompt: 'Execute the child.' }, report: 'No fake report.', nativeExecution: { modelId: 'default' } }] }), receipts, undefined, { payload: parent, flags: 0 })
    expect(updateKinds(body)).not.toContain('toolCompleted')
    expect(updateKinds(body)).toContain('execRequest')
    expect(receipts).toEqual([])
  })

  for (const text of ['completed answer', '']) {
    it(`reports the completed native turn once with text length ${text.length}`, async () => {
      const receipts: { prompt: string, text: string }[] = []
      await runStreamBody(async () => ({ text }), receipts)
      expect(receipts).toEqual([{ prompt: 'Say the mock word.', text }])
    })
  }

  it('does not report an unanswered native turn as completed', async () => {
    const receipts: { prompt: string, text: string }[] = []
    await runStreamBody(async () => undefined, receipts)
    expect(receipts).toEqual([])
  })

  it('reports the actual text from a matching native MCP result', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const text = encodeLengthDelimited(1, encodeStringField(1, 'REAL_MCP_REPLY'))
    const success = encodeLengthDelimited(1, encodeLengthDelimited(1, text))
    const reply = encodeLengthDelimited(2, Buffer.concat([
      Uint8Array.from([0x08, ...encodeVarint(301)]),
      encodeLengthDelimited(11, success),
    ]))
    await runStreamBody(async () => ({ text: 'ROOT_ANSWER', toolCalls: [{ kind: 'mcp', call: { callID: 'mcp-1', server: 'form_probe', tool: 'echo', input: {} } }] }), receipts, reply)
    expect(receipts).toEqual([{ prompt: 'Say the mock word.', text: 'ROOT_ANSWER\nREAL_MCP_REPLY' }])
  })

  /** The CLI's answer to a request context query, with the rules that it states. */
  function requestContextReply(id: number, rules: readonly { path: string, content: string }[]): Uint8Array {
    const context = Buffer.concat(rules.map(rule => encodeLengthDelimited(2, Buffer.concat([encodeStringField(1, rule.path), encodeStringField(2, rule.content)]))))
    return encodeLengthDelimited(2, Buffer.concat([
      Uint8Array.from([0x08, ...encodeVarint(id)]),
      encodeLengthDelimited(10, encodeLengthDelimited(1, encodeLengthDelimited(1, context))),
    ]))
  }

  it('asks for the request context and hands the stated rules to the turn before its text', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const stated: { path: string, content: string }[][] = []
    const reply = requestContextReply(301, [{ path: '/project/AGENTS.md', content: 'NATIVE_PROJECT_CONFIG' }])
    const body = await runStreamBody(async () => ({
      text: 'ROOT_ANSWER',
      toolCalls: [{ kind: 'requestContext', callID: 'context-1' }],
      requestContextRules: rules => stated.push(rules.map(rule => ({ ...rule }))),
    }), receipts, reply)
    expect(stated).toEqual([[{ path: '/project/AGENTS.md', content: 'NATIVE_PROJECT_CONFIG' }]])
    const kinds = updateKinds(body)
    expect(kinds.indexOf('execRequest')).toBeGreaterThanOrEqual(0)
    expect(kinds.indexOf('execRequest')).toBeLessThan(kinds.indexOf('textDelta'))
    // The query is no tool call of the transcript, and its rules are no part of the reply text.
    expect(kinds).not.toContain('toolStarted')
    expect(receipts).toEqual([{ prompt: 'Say the mock word.', text: 'ROOT_ANSWER' }])
  })

  it('states an empty rule list for a project with no rules', async () => {
    const stated: unknown[][] = []
    await runStreamBody(async () => ({
      text: 'ROOT_ANSWER',
      toolCalls: [{ kind: 'requestContext', callID: 'context-1' }],
      requestContextRules: rules => stated.push([...rules]),
    }), undefined, requestContextReply(301, []))
    expect(stated).toEqual([[]])
  })

  it('does not finish the turn while the CLI leaves its request context unanswered', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const body = await runStreamBody(async () => ({
      text: 'ROOT_ANSWER',
      toolCalls: [{ kind: 'requestContext', callID: 'context-1' }],
      requestContextRules: () => {},
    }), receipts)
    expect(updateKinds(body)).toContain('execRequest')
    expect(updateKinds(body)).not.toContain('textDelta')
    expect(receipts).toEqual([])
  })

  it('refuses a request context answer with another id, and one that no handler can take', async () => {
    const errors: string[] = []
    await expect(runStreamBody(async () => ({
      toolCalls: [{ kind: 'requestContext', callID: 'context-1' }],
      requestContextRules: () => {},
    }), undefined, requestContextReply(999, []), { payload: clientMessageWithPrompt('Say the mock word.'), flags: 0, errors })).rejects.toThrow()
    expect(errors).toEqual(['Cursor replied to request context query 301 with id 999'])

    const unhandled: string[] = []
    await expect(runStreamBody(async () => ({ toolCalls: [{ kind: 'requestContext', callID: 'context-1' }] }), undefined, requestContextReply(301, []), { payload: clientMessageWithPrompt('Say the mock word.'), flags: 0, errors: unhandled })).rejects.toThrow()
    expect(unhandled).toEqual(['A native Cursor request context query requires a scenario handler'])
  })

  it('reports the actual selected option from a native interaction reply', async () => {
    const receipts: { prompt: string, text: string }[] = []
    const answer = Buffer.concat([encodeStringField(1, 'question-1'), encodeStringField(2, 'option-red')])
    const reply = encodeLengthDelimited(6, Buffer.concat([
      Uint8Array.from([0x08, ...encodeVarint(300)]),
      encodeLengthDelimited(3, encodeLengthDelimited(1, encodeLengthDelimited(1, encodeLengthDelimited(1, answer)))),
    ]))
    await runStreamBody(async () => ({ toolCalls: [{ kind: 'question', callID: 'q-1', title: 'Color', questions: [{ id: 'question-1', prompt: 'Which color?', allowMultiple: false, options: [{ id: 'option-red', label: 'Red' }] }] }] }), receipts, reply)
    expect(receipts).toEqual([{ prompt: 'Say the mock word.', text: 'Cursor question selected: option-red' }])
  })

  for (const id of [undefined, 999]) {
    it(`adds no completion receipt for a native MCP reply with id ${id}`, async () => {
      const receipts: { prompt: string, text: string }[] = []
      const reply = encodeLengthDelimited(2, Buffer.concat([
        ...(id === undefined ? [] : [Uint8Array.from([0x08, ...encodeVarint(id)])]),
        encodeLengthDelimited(11, encodeLengthDelimited(2, encodeStringField(1, 'failure'))),
      ]))
      await expect(runStreamBody(async () => ({ toolCalls: [{ kind: 'mcp', call: { callID: 'mcp-1', server: 'form_probe', tool: 'echo', input: {} } }] }), receipts, reply)).rejects.toThrow()
      expect(receipts).toEqual([])
    })
  }

  it('adds no completion receipt when the client leaves a native request unanswered', async () => {
    const receipts: { prompt: string, text: string }[] = []
    await runStreamBody(async () => ({ toolCalls: [{ kind: 'mcp', call: { callID: 'mcp-1', server: 'form_probe', tool: 'echo', input: {} } }] }), receipts)
    expect(receipts).toEqual([])
  })

  it('writes a native transcript blob for a plain text turn so session/load can reopen it', async () => {
    const body = await runStreamBody(async () => ({ text: 'Plain answer survives the native session.' }))
    const records = takeConnectFrames(new Uint8Array(body)).frames.flatMap(({ payload: frame }) => {
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
      descend(frame.payload, [FIELD_INTERACTION_UPDATE, FIELD_TOOL_CALL_COMPLETED]) !== undefined)
    expect(completed, 'a completed update reached the stream').toBeDefined()
    expect(Buffer.from(completed!.payload).includes(Buffer.from('PONG_FROM_CHILD'))).toBe(true)
    // The blobs carry it too, and they are the half that survives the turn.
    expect(raw.includes(Buffer.from('PONG_FROM_CHILD'))).toBe(true)
  })
})

describe('native Cursor request compression', () => {
  it('reads the actual prompt from a native gzip request before choosing its answer', async () => {
    const prompts: string[] = []
    const receipts: { prompt: string, text: string }[] = []
    const body = await runStreamBody(async (prompt) => {
      prompts.push(prompt)
      return { text: 'The compressed native child answered.' }
    }, receipts, undefined, { payload: gzipSync(clientMessageWithPrompt('Say the mock word.')), flags: 1, encoding: 'gzip' })
    expect(prompts).toEqual(['Say the mock word.'])
    expect(receipts).toEqual([{ prompt: 'Say the mock word.', text: 'The compressed native child answered.' }])
    expect(updateKinds(body)).toContain('textDelta')
    expect(updateKinds(body).at(-1)).toBe('endOfStream')
  })

  it.each([
    { encoding: undefined, payload: gzipSync(clientMessageWithPrompt('Say the mock word.')), reason: /encoding|gzip/ },
    { encoding: 'br', payload: gzipSync(clientMessageWithPrompt('Say the mock word.')), reason: /encoding|br/ },
    { encoding: 'gzip', payload: Buffer.from('This is not gzip.'), reason: /gzip|header|compressed/ },
  ])('refuses an invalid native compressed request with $encoding', async ({ encoding, payload, reason }) => {
    const errors: string[] = []
    const prompts: string[] = []
    const pending = runStreamBody(async (prompt) => {
      prompts.push(prompt)
      return { text: 'An invalid compressed frame must not reach this answer.' }
    }, undefined, undefined, { payload, flags: 1, errors, ...(encoding === undefined ? {} : { encoding }) })
    await expect(pending).rejects.toThrow()
    expect(prompts).toEqual([])
    expect(errors).toHaveLength(1)
    expect(errors[0]).toMatch(reason)
  })
})

async function startOwnedCursor(host: MockModelScriptHost) {
  const surface = createCursorSurface(host)
  const server = createServer((request, response) => {
    void surface.handleHttp(request, response, new URL(request.url ?? '/', 'http://mock.invalid')).then((owned) => {
      if (!owned)
        response.writeHead(404).end()
    }).catch((error: unknown) => response.destroy(error instanceof Error ? error : new Error(String(error))))
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  close.push(() => new Promise<void>((resolve) => {
    surface.close()
    server.closeAllConnections()
    server.close(() => resolve())
  }))
  const { port } = server.address() as AddressInfo
  const url = `http://127.0.0.1:${port}`
  return {
    surface,
    url,
    run: async (conversationID: string, prompt: string) => {
      const opening = clientMessageWithPrompt(prompt)
      const runRequest = descend(opening, [1])
      if (!runRequest)
        throw new Error('The test opening frame has no Run request.')
      const nativeRun = encodeLengthDelimited(1, Buffer.concat([
        Buffer.from(runRequest),
        Buffer.from(encodeStringField(5, conversationID)),
      ]))
      const response = await fetch(`${url}${CURSOR_RUN_PATH}`, {
        method: 'POST',
        headers: { 'content-type': 'application/connect+proto' },
        body: Buffer.from(connectFrame(nativeRun)),
      })
      return Buffer.from(await response.arrayBuffer())
    },
  }
}

function cursorScriptHost(registered: Set<string>, contexts: ModelRequestContext[], answerFor: (context: ModelRequestContext) => MockModelStep | undefined = context => ({ text: context.userText })) {
  const host: MockModelScriptHost = {
    hasScenario: id => registered.has(id),
    select: (context, capabilities) => {
      contexts.push(context)
      expect(capabilities).toEqual({ allowServiceToolMetadata: true })
      const step = answerFor(context)
      if (!registered.has(context.scenarioID ?? '') || !step)
        return { kind: 'missing', message: 'No actual scripted answer.' }
      return {
        kind: 'step',
        step,
        isClosed: () => !registered.has(context.scenarioID ?? ''),
        holdGate: async () => true,
        stream: (response, request) => {
          expect(request).toBeDefined()
          return createModelStream(response, step.stream)
        },
        bufferGeneration: async () => true,
        recordHttpResponse: () => {},
        recordServiceError: () => {},
      } satisfies SelectedModelAnswer
    },
  }
  return host
}

describe('createCursorSurface', () => {
  it('preserves actual native body fields and keeps request metadata outside the body', async () => {
    const contexts: ModelRequestContext[] = []
    const owned = await startOwnedCursor(cursorScriptHost(new Set(['surface-native']), contexts))
    const prompt = mockScenarioPrompt('surface-native', 'Actual native prompt.')
    const bytes = await owned.run('actual-conversation', prompt)
    expect(updateKinds(bytes)).toContain('textDelta')
    expect(contexts).toHaveLength(1)
    expect(contexts[0]).toMatchObject({ protocol: 'openai-responses', path: CURSOR_RUN_PATH, systemText: '', userText: prompt, scenarioID: 'surface-native' })
    expect(contexts[0]?.body).toEqual({ prompt, attachments: [], conversationId: 'actual-conversation' })
    expect(contexts[0]?.nativeRequest).toBeDefined()
    expect(contexts[0]?.serverContext).toEqual({ conversationId: 'actual-conversation', messages: [] })
  })

  it('retains registered exhausted context and does not route an unregistered conversation', async () => {
    const contexts: ModelRequestContext[] = []
    const registered = new Set(['registered-exhausted'])
    let supply = false
    const owned = await startOwnedCursor(cursorScriptHost(registered, contexts, context => supply ? { text: context.userText } : undefined))
    await owned.run('exhausted-id', mockScenarioPrompt('registered-exhausted', 'No step remains.'))
    await owned.run('unregistered-id', mockScenarioPrompt('unregistered-now', 'This scenario does not exist.'))
    registered.add('unregistered-now')
    supply = true
    await owned.run('exhausted-id', 'Bare registered continuation.')
    await owned.run('unregistered-id', 'Bare unknown continuation.')
    expect(contexts[2]).toHaveProperty('scenarioID', 'registered-exhausted')
    expect(contexts[2]?.serverContext).toEqual({ conversationId: 'exhausted-id', messages: [] })
    expect(contexts[3]).toHaveProperty('scenarioID', 'ambient')
    expect(contexts[3]).not.toHaveProperty('serverContext')
  })

  it('separates native IDs and clears both completed history and scenario routing', async () => {
    const contexts: ModelRequestContext[] = []
    const registered = new Set(['surface-history'])
    const owned = await startOwnedCursor(cursorScriptHost(registered, contexts))
    const first = mockScenarioPrompt('surface-history', 'First native turn.')
    await owned.run('one', first)
    await owned.run('two', mockScenarioPrompt('surface-history', 'Other native turn.'))
    await owned.run('one', 'Bare next turn.')
    expect(contexts[1]?.serverContext?.messages).toEqual([])
    expect(contexts[2]?.serverContext?.messages).toEqual([{ role: 'user', content: first }, { role: 'assistant', content: first }])
    registered.delete('surface-history')
    owned.surface.clearScenario('surface-history')
    await owned.run('one', 'After scenario deletion.')
    expect(contexts[3]).toHaveProperty('scenarioID', 'ambient')
    expect(contexts[3]).not.toHaveProperty('serverContext')
    registered.add('surface-history')
    await owned.run('one', mockScenarioPrompt('surface-history', 'Fresh registered turn.'))
    expect(contexts[4]?.serverContext?.messages).toEqual([])
    owned.surface.close()
    await owned.run('one', 'After Surface close.')
    expect(contexts[5]).toHaveProperty('scenarioID', 'ambient')
    expect(contexts[5]).not.toHaveProperty('serverContext')
  })

  it('leaves unrelated paths unanswered and never selects a script for startup calls', async () => {
    const contexts: ModelRequestContext[] = []
    const owned = await startOwnedCursor(cursorScriptHost(new Set(), contexts))
    expect((await fetch(`${owned.url}/v1/chat/completions`)).status).toBe(404)
    const startup = await fetch(`${owned.url}/aiserver.v1.AiService/GetUserSettings`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' })
    expect(startup.status).toBe(200)
    expect(await startup.json()).toEqual({})
    expect(contexts).toEqual([])
  })
})
