import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { resolveMessageForRendering } from '../../registry'
import { classifyLettaMessage } from '../classification'
import { LETTA_TOOL_REQUEST_OVERRIDES, lettaExtractRow } from './row'

function resolvedLettaFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.LETTA)
}

describe('lettaExtractRow', () => {
  function nativeToolRow(options: { name: string, args: unknown, status?: string, result?: unknown, resultId?: string, completion?: MessageCompletion, role?: 'request' | 'result' }) {
    const request = resolvedLettaFrame({ message_type: 'client_tool_start', tool_call_id: 'actual-tool', tool_name: options.name, tool_args: options.args })
    const result = options.status === undefined
      ? undefined
      : resolvedLettaFrame({
          message_type: 'tool_return_message',
          tool_call_id: options.resultId ?? 'actual-tool',
          status: options.status,
          tool_return: options.result ?? '',
        })
    const role = options.role ?? 'request'
    const selected = role === 'result' && result ? result : request
    const row = lettaExtractRow({
      resolved: selected,
      category: classifyLettaMessage({ ...selected, agentProvider: AgentProvider.LETTA }),
      span: { request, result, role, visibleRows: { request: true, result: result !== undefined } },
      ...(options.completion !== undefined ? { completion: options.completion } : {}),
    })
    expect(row?.kind).toBe('tool')
    if (!row || row.kind !== 'tool')
      throw new Error('The actual Letta tool frame produced no tool row.')
    return row
  }

  it('uses actual serialized client arguments and matching success to draw the applied edit', () => {
    const row = nativeToolRow({
      name: 'Edit',
      args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }),
      status: 'success',
      result: '{"message":"Successfully replaced 1 occurrence","replacements":1,"startLine":1}',
    })
    expect(row.call.kind).toBe('edit')
    expect(row.call.name).toBe('Edit')
    expect(row.call.id).toBe('actual-tool')
    expect(row.call.status).toBe('completed')
    expect(row.call.request).toMatchObject({ changes: [{ filePath: '/private/native.txt', oldStr: 'OLD42', newStr: 'NEW42' }] })
    expect(row.call.result).toMatchObject({ changes: [{ filePath: '/private/native.txt', oldStr: 'OLD42', newStr: 'NEW42' }] })
  })

  it('keeps native error status even when Bash stderr contains no failure words', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'printf computed >&2; exit 7' }), status: 'error', result: 'SHELLERR77', role: 'result' })
    expect(row.call.name).toBe('Bash')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: 'SHELLERR77' })
  })

  it('preserves an empty replacement in a completed native edit', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: '' }), status: 'success', result: 'The replacement completed.' })
    expect(row.call.kind).toBe('edit')
    expect(row.call.result).toMatchObject({ changes: [{ oldStr: 'OLD42', newStr: '' }] })
  })

  it('does not apply an unrelated completed tool result to this edit', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status: 'success', result: 'OTHER_RESULT', resultId: 'another-tool' })
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it.each(['error', 'success'])('keeps a retained interruption distinct from native %s', (status) => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status, result: 'native result', completion: MessageCompletion.INTERRUPTED })
    expect(row.call.status).toBe('cancelled')
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
  })

  it('keeps a failed native edit from reporting applied changes', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status: 'error', result: 'No matching input.' })
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: 'No matching input.' })
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
  })

  it('preserves successful empty native output as a completed result', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'exit 0' }), status: 'success', result: '', role: 'result' })
    expect(row.call.status).toBe('completed')
    expect(row.call.name).toBe('Bash')
    expect(row.call.result).toBeDefined()
  })

  it.each(['{', 'null', '[]', '', false])('does not invent an applied diff from malformed native arguments: %j', (args) => {
    const row = nativeToolRow({ name: 'Edit', args, status: 'success', result: 'A native result.' })
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
    expect(row.call.request).not.toHaveProperty('changes')
  })

  it('builds the declared Read request from the native file_path argument', () => {
    const row = nativeToolRow({ name: 'Read', args: JSON.stringify({ file_path: '/private/note.txt', offset: 2, limit: 5 }) })
    expect(row.call.kind).toBe('read')
    expect(row.call.request).toEqual({ path: '/private/note.txt', offset: 2, limit: 5 })
  })

  it('builds the declared question request from the native AskUserQuestion arguments', () => {
    const row = nativeToolRow({
      name: 'AskUserQuestion',
      args: JSON.stringify({ questions: [{ header: 'Color', question: 'Which color?', options: [{ label: 'Red', description: 'The warm one' }, { label: 'Blue' }], multiSelect: false }] }),
    })
    expect(row.call.kind).toBe('question')
    expect(row.call.request).toEqual({ questions: [{ header: 'Color', question: 'Which color?', options: [{ label: 'Red', description: 'The warm one' }, { label: 'Blue' }] }] })
  })

  // Letta Code 0.34 posts the questions of an AskUserQuestion call and returns a receipt at
  // once. The receipt is JSON text. Its `message` is the one sentence for the reader: the
  // questions already show in the request, and the answer arrives later as a control answer.
  // Both values are verbatim from a live Letta Code 0.34.2 run (`tool_args` and `tool_return`
  // of the `question` probe).
  const QUESTION_ARGS = '{"questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]}]}'
  const QUESTION_RECEIPT_MESSAGE = 'Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer.'
  const QUESTION_RECEIPT = `{"type":"ask_user_question","version":2,"toolCallId":"actual-tool","questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]}],"message":"${QUESTION_RECEIPT_MESSAGE}"}`

  it('draws only the message of a question receipt on the question row', () => {
    const row = nativeToolRow({ name: 'AskUserQuestion', args: QUESTION_ARGS, status: 'success', result: QUESTION_RECEIPT })
    expect(row.call.kind).toBe('question')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ unparsed: true, text: QUESTION_RECEIPT_MESSAGE })
  })

  it('draws only the message of a question receipt on the result row', () => {
    const row = nativeToolRow({ name: 'AskUserQuestion', args: QUESTION_ARGS, status: 'success', result: QUESTION_RECEIPT, role: 'result' })
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ content: [{ type: 'text', text: QUESTION_RECEIPT_MESSAGE }] })
  })

  it('draws the message of a question receipt that arrives as an object', () => {
    const row = nativeToolRow({ name: 'AskUserQuestion', args: QUESTION_ARGS, status: 'success', result: JSON.parse(QUESTION_RECEIPT), role: 'result' })
    expect(row.call.result).toEqual({ content: [{ type: 'text', text: QUESTION_RECEIPT_MESSAGE }] })
  })

  it.each([
    ['the text of a refused call', 'Tool not found: AskUserQuestion. Available tools: Bash, Monitor'],
    ['a receipt with no message', '{"type":"ask_user_question","version":2,"toolCallId":"actual-tool","questions":[]}'],
    ['a JSON list', '[{"type":"ask_user_question","message":"Questions posted."}]'],
    ['JSON null', 'null'],
  ])('keeps %s as Letta Code wrote it', (_name, result) => {
    const row = nativeToolRow({ name: 'AskUserQuestion', args: QUESTION_ARGS, status: 'success', result, role: 'result' })
    expect(row.call.result).toEqual({ content: [{ type: 'text', text: result }] })
  })

  it('keeps the text of a failed question call', () => {
    const row = nativeToolRow({ name: 'AskUserQuestion', args: QUESTION_ARGS, status: 'error', result: 'The question tool failed.', role: 'result' })
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toEqual({ failure: true, text: 'The question tool failed.' })
  })

  it('keeps the bytes that another tool returns, although they match a question receipt', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'cat receipt.json' }), status: 'success', result: QUESTION_RECEIPT, role: 'result' })
    expect(row.call.result).toEqual({ content: [{ type: 'text', text: QUESTION_RECEIPT }] })
  })

  // A kind renderer reads its own request fields without a guard. Raw arguments carry
  // none of them for these tools, so each row reads its request through the shared table.
  it.each([
    ['Agent', 'agent', { description: 'Read the file', prompt: 'Read it.', subagent_type: 'general-purpose' }, { description: 'Read the file', prompt: 'Read it.' }],
    ['SendAgentMessage', 'message', { to: 'agent-1', message: 'Hello.' }, { to: 'agent-1', text: 'Hello.' }],
    ['TaskStop', 'task', { task_id: 'task_3' }, { action: 'other', taskId: 'task_3' }],
    ['Monitor', 'trigger', { name: 'ci', schedule: '*/5 * * * *', command: 'make test' }, { action: 'other', name: 'ci', schedule: '*/5 * * * *' }],
  ])('reads the declared request of a %s call', (name, kind, args, request) => {
    const row = nativeToolRow({ name, args: JSON.stringify(args) })
    expect(row.call.kind).toBe(kind)
    expect(row.call.request).toEqual(request)
  })

  it('keeps a returned declared-kind answer as plain text on the request row', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'printf done', description: 'Print done' }), status: 'success', result: 'done' })
    expect(row.call.kind).toBe('execute')
    expect(row.call.request).toEqual({ command: 'printf done', description: 'Print done' })
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ unparsed: true, text: 'done' })
  })

  it('shows the native child Read call with its exact tool id and path', () => {
    const request = resolvedLettaFrame({
      type: 'message',
      message_type: 'tool_call_message',
      tool_calls: [{ tool_call_id: 'call-read-native', name: 'Read', arguments: '{"file_path":"note.txt"}' }],
    })
    const span: ToolSpanContext = { request, result: undefined, role: 'request', visibleRows: { request: true, result: false } }
    const row = lettaExtractRow({ resolved: request, category: classifyLettaMessage({ ...request, agentProvider: AgentProvider.LETTA }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.id).toBe('call-read-native')
    expect(row.call.name).toBe('Read')
    expect(JSON.stringify(row.call.request)).toContain('note.txt')
  })
})

function pairedLettaNativeRow(requestFrame: Record<string, unknown>, resultFrame: Record<string, unknown>, role: 'request' | 'result') {
  const request = resolvedLettaFrame(requestFrame)
  const result = resolvedLettaFrame(resultFrame)
  const selected = role === 'result' ? result : request
  const row = lettaExtractRow({
    resolved: selected,
    category: classifyLettaMessage({ ...selected, agentProvider: AgentProvider.LETTA }),
    span: { request, result, role, visibleRows: { request: true, result: true } },
  })
  if (!row || row.kind !== 'tool')
    throw new Error('The native Letta frames must retain their tool row.')
  return row
}

describe('letta native result boundaries', () => {
  const request = { message_type: 'client_tool_start', tool_call_id: 'call', run_id: 'current-run', tool_name: 'Edit', tool_args: '{"file_path":"native.txt","old_string":"old","new_string":"new"}' }
  const result = { message_type: 'tool_return_message', tool_call_id: 'call', run_id: 'current-run', status: 'success', tool_return: 'native returned data' }

  it('keeps a current-window snapshot from completing the paired request', () => {
    const progress = { ...result, id: 'synthetic-tool-return-stream-call' }
    const row = pairedLettaNativeRow(request, progress, 'request')
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it.each(['old-run', undefined])('keeps a different or absent native run from applying a result: %s', (run_id) => {
    const row = pairedLettaNativeRow(request, { ...result, run_id }, 'request')
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it('retains an actual old-run final without pairing the new request arguments', () => {
    const row = pairedLettaNativeRow(request, { ...result, run_id: 'old-run', id: 'synthetic-tool-return-actual-final' }, 'result')
    expect(row.call.status).toBe('completed')
    expect(row.call.kind).toBe('other')
    expect(row.call.result).not.toHaveProperty('changes')
    expect(row.call.result).toMatchObject({ content: [{ type: 'text', text: 'native returned data' }] })
  })

  it.each(['', 0, false, null])('retains present client-end output without replacing %j', (tool_return) => {
    const row = pairedLettaNativeRow({ ...request, tool_name: 'Bash', tool_args: '{"command":"native command"}' }, { ...result, message_type: 'client_tool_end', tool_return }, 'result')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toMatchObject({ content: [{ type: 'text', text: typeof tool_return === 'string' ? tool_return : JSON.stringify(tool_return) }] })
  })

  it('retains a matching composite error and its zero output', () => {
    const native = { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', tool_returns: [{ tool_call_id: 'call', status: 'error', tool_return: 0 }] }
    const row = pairedLettaNativeRow(request, native, 'result')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: '0' })
  })

  it.each([
    { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', status: 'success' },
    { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', status: 'success', tool_returns: [{ tool_call_id: 'foreign', tool_return: 'foreign data' }] },
  ])('retains an incomplete native final without inventing returned data: %j', (native) => {
    const row = pairedLettaNativeRow({ ...request, tool_name: 'Bash', tool_args: '{"command":"native command"}' }, native, 'result')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })
})

describe('letta native task rows', () => {
  // The exact frames Letta Code 0.34.2 sends for TaskCreate, TaskUpdate, TaskGet,
  // TaskList and UpdatePlan: `client_tool_start` with serialized `tool_args`, then a
  // `tool_return_message` whose `tool_return` serializes the task record.
  const createArgs = '{"subject":"Inspect the repository","description":"Read the repository files.","activeForm":"Inspecting the repository"}'
  const firstRecord = { taskId: 'task_1', subject: 'Inspect the repository', description: 'Read the repository files.', activeForm: 'Inspecting the repository', status: 'pending', blocks: [], blockedBy: [], metadata: {}, createdAt: 1, updatedAt: 1 }
  const secondRecord = { taskId: 'task_2', subject: 'List three checks', description: 'List three checks to run.', status: 'in_progress', blocks: [], blockedBy: [], metadata: {}, createdAt: 1, updatedAt: 2 }

  function taskStart(name: string, args: string, callId = 'task-call') {
    return { message_type: 'client_tool_start', tool_call_id: callId, run_id: 'local-run-1', tool_name: name, tool_args: args }
  }

  function taskReturn(toolReturn: unknown, status = 'success', callId = 'task-call') {
    return { message_type: 'tool_return_message', tool_call_id: callId, run_id: 'local-run-1', status, tool_return: typeof toolReturn === 'string' ? toolReturn : JSON.stringify(toolReturn) }
  }

  function liveTaskRow(name: string, args: string) {
    const request = resolvedLettaFrame(taskStart(name, args))
    const row = lettaExtractRow({
      resolved: request,
      category: classifyLettaMessage({ ...request, agentProvider: AgentProvider.LETTA }),
      span: { request, result: undefined, role: 'request', visibleRows: { request: true, result: false } },
    })
    if (!row || row.kind !== 'tool')
      throw new Error('The native Letta task frame must produce a tool row.')
    return row
  }

  it('draws the task a running TaskCreate states before its answer lands', () => {
    const row = liveTaskRow('TaskCreate', createArgs)
    expect(row.call.kind).toBe('todo')
    // A native start states no status of its own, and no answer finished the call yet.
    expect(row.call.status).toBe('unstated')
    expect(row.call.request).toEqual({
      items: [{ rowKey: '0:Inspect the repository', content: 'Inspect the repository', status: 'pending', activeForm: 'Inspecting the repository' }],
      note: 'Read the repository files.',
    })
    expect(row.call.result).toBeUndefined()
  })

  it('reads the saved task from the native TaskCreate answer on both rows', () => {
    const saved = { items: [{ id: 'task_1', rowKey: 'task_1', content: 'Inspect the repository', status: 'pending', activeForm: 'Inspecting the repository' }], note: 'Read the repository files.' }
    for (const role of ['request', 'result'] as const) {
      const row = pairedLettaNativeRow(taskStart('TaskCreate', createArgs), taskReturn(firstRecord), role)
      expect(row.call.kind).toBe('todo')
      expect(row.call.status).toBe('completed')
      expect(row.call.result).toEqual(saved)
    }
  })

  it('reads the patched task from the native TaskUpdate answer', () => {
    const record = { ...firstRecord, status: 'completed', updatedAt: 3 }
    const row = pairedLettaNativeRow(taskStart('TaskUpdate', '{"taskId":"task_1","status":"completed"}'), taskReturn(record), 'result')
    expect(row.call.kind).toBe('todo')
    expect(row.call.request).toEqual({ items: [{ id: 'task_1', rowKey: 'task_1', content: 'Inspect the repository', status: 'completed', activeForm: 'Inspecting the repository' }], note: 'Read the repository files.' })
    expect(row.call.result).toEqual(row.call.request)
  })

  it('reads the task a TaskGet answer returns', () => {
    const row = pairedLettaNativeRow(taskStart('TaskGet', '{"taskId":"task_2"}'), taskReturn(secondRecord), 'request')
    expect(row.call.kind).toBe('todo')
    expect(row.call.result).toEqual({ items: [{ id: 'task_2', rowKey: 'task_2', content: 'List three checks', status: 'in_progress', activeForm: '' }], note: 'List three checks to run.' })
  })

  it('reads the whole list from a native TaskList answer', () => {
    const row = pairedLettaNativeRow(taskStart('TaskList', '{}'), taskReturn({ tasks: [{ ...firstRecord, status: 'completed' }, secondRecord] }), 'result')
    expect(row.call.kind).toBe('todo')
    expect(row.call.result).toEqual({ items: [
      { id: 'task_1', rowKey: 'task_1', content: 'Inspect the repository', status: 'completed', activeForm: 'Inspecting the repository', description: 'Read the repository files.' },
      { id: 'task_2', rowKey: 'task_2', content: 'List three checks', status: 'in_progress', activeForm: '', description: 'List three checks to run.' },
    ] })
  })

  it('reads an empty native TaskList answer as an empty list', () => {
    const row = pairedLettaNativeRow(taskStart('TaskList', '{}'), taskReturn({ tasks: [] }), 'result')
    expect(row.call.kind).toBe('todo')
    expect(row.call.result).toEqual({ items: [] })
  })

  it('reads the steps of an UpdatePlan call and keeps them as the saved plan', () => {
    const args = '{"explanation":"Start with the parser.","plan":[{"step":"Read the parser","status":"completed"},{"step":"Fix the tokenizer","status":"in_progress"}]}'
    const plan = { items: [
      { rowKey: '0:Read the parser', content: 'Read the parser', status: 'completed', activeForm: '' },
      { rowKey: '1:Fix the tokenizer', content: 'Fix the tokenizer', status: 'in_progress', activeForm: '' },
    ], note: 'Start with the parser.' }
    expect(liveTaskRow('UpdatePlan', args).call.request).toEqual(plan)
    const row = pairedLettaNativeRow(taskStart('UpdatePlan', args), taskReturn({ message: 'Plan updated' }), 'result')
    expect(row.call.kind).toBe('todo')
    expect(row.call.result).toEqual(plan)
  })

  it.each([
    ['TaskUpdate', '{"taskId":"task_1","status":"completed"}'],
    ['TaskGet', '{"taskId":"task_1"}'],
    ['TaskList', '{}'],
  ])('keeps a running %s that states no task on the generic card', (name, args) => {
    const row = liveTaskRow(name, args)
    expect(row.call.kind).toBe('other')
    expect(row.call.request).toEqual({ args: JSON.parse(args) })
  })

  it('keeps the native failure of a task call beside the task it asked for', () => {
    const row = pairedLettaNativeRow(taskStart('TaskCreate', createArgs), taskReturn('TaskCreate: \'subject\' must be a non-empty string', 'error'), 'request')
    expect(row.call.kind).toBe('todo')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toEqual({ failure: true, text: 'TaskCreate: \'subject\' must be a non-empty string' })
  })

  it.each([
    ['malformed JSON', 'not JSON'],
    ['a record with no subject', { taskId: 'task_1', status: 'pending' }],
    ['a list that is not an array', { tasks: 'none' }],
  ])('keeps the answer as plain text when it holds %s', (_label, answer) => {
    const row = pairedLettaNativeRow(taskStart('TaskCreate', createArgs), taskReturn(answer), 'request')
    expect(row.call.kind).toBe('todo')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ unparsed: true, text: typeof answer === 'string' ? answer : JSON.stringify(answer) })
  })
})

describe('LETTA_TOOL_REQUEST_OVERRIDES', () => {
  it('deviates on exactly the one kind whose Letta arguments the shared table cannot read', () => {
    expect(Object.keys(LETTA_TOOL_REQUEST_OVERRIDES)).toEqual(['question'])
  })

  it('keeps the questions that the shared table drops', () => {
    const args = { questions: [{ question: 'Which color?', options: [{ label: 'Red' }, { description: 'No label' }] }, { header: 'Empty', question: '' }] }
    expect(LETTA_TOOL_REQUEST_OVERRIDES.question?.(args, null)).toEqual({ questions: [{ question: 'Which color?', options: [{ label: 'Red' }] }] })
    expect(DEFAULT_TOOL_REQUESTS.question(args)).toEqual({ questions: [] })
  })
})
