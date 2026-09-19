import type { FileEditDiff } from '../../../ir/fileEditDiff'
import type { ToolCallIR } from '../../../ir/toolCall'
import type { ToolKind } from '../../../ir/toolKind'
import type { ZCodeRow } from '../extractors/toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { todoTitleOf } from '~/test-support/toolCallIr'
import { toolCallRow } from '../../../ir/row'
import { isUnparsedResult, typedResult } from '../../../ir/toolCall'
import { TOOL_KINDS } from '../../../ir/toolKind'
import { toolCallMeta } from '../../../results/tools/meta'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { zcodeExtractTool, zcodeRow } from '../extractors/toolCommon'
import { ZCODE_DISPLAY } from '../protocol'
import { zcodeToolKind } from '../toolKinds'
import { ZCODE_TOOL_READERS, ZCODE_TOOL_REQUEST_OVERRIDES, zcodeReclassify, zcodeToolCallIR, zcodeToolFacts } from './toolCall'

function event(kind: string, fields: Record<string, unknown>): Record<string, unknown> {
  return { type: 'tool.updated', payload: { kind, toolCallId: 'call', ...fields } }
}

function row(parsed: Record<string, unknown>, spanType?: string, request?: Record<string, unknown>): ZCodeRow {
  return zcodeRow(parsed, spanType, request ? input(request) : undefined)
}

function resultRow(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>): ZCodeRow {
  return row(event(ZCODE_TOOL_KIND.Result, { result }), toolName, event(ZCODE_TOOL_KIND.Scheduled, { toolName, input: args }))
}

function presentationOf(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>) {
  return zcodeToolCallIR(resultRow(toolName, args, result))!
}

describe('zcodeToolKind', () => {
  it.each([
    [ZCODE_TOOL.Bash, 'execute'],
    [ZCODE_TOOL.Read, 'read'],
    [ZCODE_TOOL.Write, 'write'],
    [ZCODE_TOOL.Edit, 'edit'],
    [ZCODE_TOOL.Glob, 'glob'],
    [ZCODE_TOOL.Grep, 'grep'],
    [ZCODE_TOOL.Agent, 'agent'],
    [ZCODE_TOOL.TodoWrite, 'todo'],
    [ZCODE_TOOL.WebFetch, 'fetch'],
  ] as const)('maps %s to the %s kind', (toolName, kind) => {
    expect(zcodeToolKind(toolName)).toBe(kind)
  })

  it('leaves an unknown tool uncategorized', () => {
    expect(zcodeToolKind('SomeToolAddedLater')).toBe('other')
  })

  // A plain object answers `toString` from its prototype, which would give the row a
  // function where a kind belongs.
  it.each(['constructor', 'toString', '__proto__'])('leaves a tool called %s uncategorized', (toolName) => {
    expect(zcodeToolKind(toolName)).toBe('other')
  })

  it('states no kind for a row that carries no tool name', () => {
    expect(zcodeToolKind('')).toBe('')
  })
})

describe('zcodeToolCallIR bodies', () => {
  it('maps an MCP display to the shared rich-content pair and recovers its arguments', () => {
    const call = presentationOf('mcp__docs__lookup', { query: 'renderer' }, {
      success: true,
      content: 'The tool response',
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' },
    })
    expect(call.kind).toBe('mcp')
    expect(call.kind === 'mcp' && call.request.server).toBe('docs')
    expect(JSON.stringify(call.request)).toContain('renderer')
  })

  it('maps a task-stop display to the shared status result', () => {
    const call = presentationOf(ZCODE_TOOL.TaskOutput, {}, {
      success: true,
      display: { kind: 'task_stop', taskId: 'task-42', command: 'npm run dev', message: 'Stopped the task' },
    })
    expect(call.kind).toBe('task')
    expect(call.kind === 'task' && call.result).toMatchObject({
      title: 'Stopped task task-42',
      outcome: 'stopped',
      command: 'npm run dev',
      output: 'Stopped the task',
    })
  })

  it('maps a failed message display to the failed status outcome', () => {
    const call = presentationOf('SendMessage', {}, {
      success: true,
      display: { kind: 'local_agent_message', status: 'failed', error: 'Peer unavailable' },
    })
    expect(call.kind).toBe('message')
    // The outcome word lives on the call now, and the display's words on its result.
    expect(call.status).toBe('failed')
    expect(call.result).toEqual({ failure: true, text: 'Failed\nPeer unavailable' })
  })

  // An image display carries its pictures beside the text, which the shared image list
  // draws. The body therefore stays the plain text of the result.
  it('keeps a node-image display as text and reports its images separately', () => {
    const parsed = event(ZCODE_TOOL_KIND.Result, {
      result: { success: true, content: 'Rendered the chart', display: { kind: 'node_repl_images', images: [{ base64: 'AAAA', mimeType: 'image/png' }] } },
    })
    const call = zcodeToolCallIR(row(parsed, 'js'))!
    expect(call.images).toHaveLength(1)
    expect(call.kind === 'execute' && call.result && isUnparsedResult(call.result) ? call.result.text : undefined).toBe('Rendered the chart')
  })

  it('copies a numbered read without the native line-number prefixes', () => {
    const call = presentationOf(ZCODE_TOOL.Read, { file_path: '/project/a.ts' }, { success: true, content: '1\tfirst\n2\tsecond' })
    const meta = toolCallMeta(toolCallRow(call, 'result', { request: false, result: false }))
    expect(meta.copyableContent()).toBe('first' + '\n' + 'second')
  })

  it('states the failure text of a failed call', () => {
    const call = presentationOf(ZCODE_TOOL.Read, { file_path: '/project/missing.ts' }, { success: false, content: 'File does not exist' })
    expect(call.result).toEqual({ failure: true, text: 'File does not exist' })
  })

  it('draws the checklist of an open TodoWrite request', () => {
    const parsed = event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.TodoWrite, input: { todos: [{ content: 'Write the parser', status: 'pending' }] } })
    const call = zcodeToolCallIR(row(parsed))!
    expect(call.kind).toBe('todo')
    // `todoRenderer` composes the count from the request; the payload states none.
    expect(call.title).toBeUndefined()
    expect(todoTitleOf(call)).toBe('1 task')
    expect(call.kind === 'todo' && call.request.items).toHaveLength(1)
  })
})

describe('zcodeToolCallIR truncation', () => {
  it('leaves the notice to the row for a body that cannot state it', () => {
    const call = presentationOf(ZCODE_TOOL.Edit, {}, {
      success: true,
      truncated: true,
      display: { kind: 'file_diff', filePath: '/project/a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }] },
    })
    expect(call.truncated).toBe(true)
  })

  // The command body draws its own notice, so a second flag on the row would print
  // `Output truncated` twice under one result.
  it('gives the flag to the command body, not to the row', () => {
    const call = presentationOf(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, truncated: true, content: 'partial' })
    expect(call.truncated).toBeUndefined()
    const source = call.kind === 'execute' && call.result && 'commands' in call.result ? call.result.commands[0] : undefined
    expect(source?.truncated).toBe(true)
  })

  // The rich-content body replaces the whole row with the shared card, which draws no
  // part of the presentation around it.
  it('keeps no flag for a rich-content body, which no notice can reach', () => {
    const call = presentationOf('mcp__docs__lookup', {}, {
      success: true,
      truncated: true,
      content: 'The tool response',
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' },
    })
    expect(call.truncated).toBeUndefined()
  })

  it('gives the flag to the search body, not to the row', () => {
    const call = presentationOf(ZCODE_TOOL.Glob, { pattern: '*.ts' }, { success: true, truncated: true, content: 'a.ts' })
    expect(call.truncated).toBeUndefined()
    expect(call.kind === 'glob' && call.result ? (call.result as { truncated?: boolean }).truncated : undefined).toBe(true)
  })
})

describe('zcodeToolCallIR', () => {
  it('reads a scheduled row as the request of its span', () => {
    const call = zcodeToolCallIR(row(event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, input: { command: 'ls' } })))!
    expect(call).toMatchObject({ id: 'call', status: '' })
    expect(call.label).toBe(ZCODE_TOOL.Bash)
  })

  it('reads a result row as the end of its span', () => {
    const call = zcodeToolCallIR(resultRow(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: 'a.ts' }))!
    expect(call).toMatchObject({ status: 'completed' })
  })

  it('reports a failed call', () => {
    const call = zcodeToolCallIR(resultRow(ZCODE_TOOL.Read, {}, { success: false, content: 'gone' }))!
    expect(call.status).toBe('failed')
  })

  // A turn that ends while the call runs stores the agent's own last frame, which
  // still reads as a call in progress.
  it('reads a retained frame as a cancelled result', () => {
    const parsed: ParsedMessageContent = { ...input(event(ZCODE_TOOL_KIND.Progress, { stdoutTail: 'partial output' })), completion: MessageCompletion.INTERRUPTED }
    const call = zcodeToolCallIR(row(parsed.parentObject!, ZCODE_TOOL.Bash), parsed)!
    expect(call).toMatchObject({ status: 'cancelled' })
    expect(call.kind).toBe('execute')
  })

  it('invents no tool name for a row that states none', () => {
    const call = zcodeToolCallIR(row(event(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'recovered output' } })))!
    expect(call.kind).toBe('other')
    expect(call.label).toBeUndefined()
  })
})

// `TaskOutput` reads a background task from the runtime, which the `task` kind
// states -- so the row takes that kind's own icon and needs no override. It used to
// fall to `other` and draw the wrench every unrecognized tool draws, and an override
// here was what kept the background-task rows pickable out of a transcript.
describe('the icon of a tool the shared kinds do state', () => {
  it('takes the shared icon for TaskOutput', () => {
    const call = presentationOf(ZCODE_TOOL.TaskOutput, {}, { success: true, content: 'done' })
    expect(call.kind).toBe('task')
    expect(call.icon).toBeUndefined()
  })

  it('leaves every kind the table maps to the shared icon', () => {
    const call = presentationOf(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: '' })
    expect(call.kind).toBe('execute')
    expect(call.icon).toBeUndefined()
  })
})

// The question, its options, each option's sentence and each option's preview all
// reached the reader as a pretty-printed JSON object before this. The control banner
// above the row drew the same question properly, so one interaction read two
// different ways in the same transcript.
describe('zcode AskUserQuestion rows', () => {
  const QUESTION = {
    questions: [{
      header: 'Parser',
      question: 'Which parser should I write?',
      multiSelect: false,
      options: [
        { label: 'Recursive descent', value: 'Recursive descent', description: 'Hand-written, easy to step through.' },
        { label: 'PEG', value: 'PEG', preview: '```ts\nconst grammar = peg`...`\n```' },
      ],
    }],
  }

  it('draws the question the scheduled row asked', () => {
    const scheduled = row(event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.AskUserQuestion, input: QUESTION }), ZCODE_TOOL.AskUserQuestion)
    const call = zcodeToolCallIR(scheduled)!
    expect(call.kind).toBe('question')
    expect(call.kind === 'question' && call.request.questions[0]?.question).toBe('Which parser should I write?')
    expect(call.kind === 'question' && call.request.questions[0]?.options).toHaveLength(2)
  })

  // A question with no option is still a question, and the row states it rather than
  // repeating the input as JSON.
  it('draws a question that offered no option', () => {
    const scheduled = row(
      event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.AskUserQuestion, input: { questions: [{ question: 'What next?' }] } }),
      ZCODE_TOOL.AskUserQuestion,
    )
    const call = zcodeToolCallIR(scheduled)!
    expect(call.kind === 'question' && call.request.questions[0]?.question).toBe('What next?')
  })

  // An opening frame that carries no input yet has no question to draw, and the row
  // must not claim one.
  it('draws plain text before the question arrives', () => {
    const scheduled = row(event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.AskUserQuestion, input: {} }), ZCODE_TOOL.AskUserQuestion)
    const call = zcodeToolCallIR(scheduled)!
    expect(call.kind === 'question' && call.request.questions).toEqual([])
  })

  // The FINISHED row draws the answer, not the question -- the same rule Claude's
  // own question rows follow. Repeating the options under a choice the reader
  // already made states nothing they can act on, and the answer is what a reader
  // who scrolls back looks for.
  it('draws the answer on the finished row', () => {
    const call = presentationOf(ZCODE_TOOL.AskUserQuestion, QUESTION, { success: true, content: 'Recursive descent' })
    expect(call.kind === 'question' && call.result ? (call.result as { answers: Array<{ answer: string | null }> }).answers[0]?.answer : undefined).toBe('Recursive descent')
  })
})

describe('zcodeToolCallIR generic rows', () => {
  // The pictures ride INSIDE the content: `GenericToolBody` never reads the call's
  // own image list, so a node-image row attached them where nothing draws them.
  it('carries a node-image row pictures inside its content', () => {
    const call = presentationOf(ZCODE_TOOL.Js, { command: 'draw()' }, {
      success: true,
      content: 'Rendered',
      display: { kind: 'node_repl_images', images: [{ data: 'aGk=', mimeType: 'image/png' }] },
    })
    // The JavaScript sandbox is an execute row, so the picture rides its own list.
    expect(call.images.length).toBe(1)
  })

  it('carries an unrecognized row pictures inside its content', () => {
    const call = presentationOf('some_unknown_tool', {}, {
      success: true,
      content: 'Rendered',
      display: { kind: 'node_repl_images', images: [{ data: 'aGk=', mimeType: 'image/png' }] },
    })
    expect(call.kind).toBe('other')
    expect(call.images).toEqual([])
    const source = call.kind === 'other' && call.result && 'content' in call.result ? call.result.content : []
    expect(source.map(item => item.type)).toEqual(['text', 'image'])
  })
})

describe('zcodeToolCallIR to-do rows', () => {
  // The demotion belongs to the kind, which is what stops the payload build from
  // answering one kind and rewriting it two lines later.
  it('reads a TodoWrite whose input carries no list as the generic row', () => {
    const call = presentationOf(ZCODE_TOOL.TodoWrite, {}, { success: true, content: 'Saved' })
    expect(call.kind).toBe('other')
  })

  it('reads a TodoWrite that carries a list as a to-do row', () => {
    const call = presentationOf(ZCODE_TOOL.TodoWrite, { todos: [{ content: 'Write the parser', status: 'pending' }] }, { success: true })
    expect(call.kind).toBe('todo')
  })
})

describe('zcodeToolCallIR truncated bodies', () => {
  // A read, a fetch, an agent, a to-do and a task body all state nothing about a cut,
  // so the row's own flag is the only notice the reader gets.
  it.each([
    [ZCODE_TOOL.Read, { file_path: '/p/a.ts' }, { success: true, truncated: true, content: 'partial' }],
    [ZCODE_TOOL.WebFetch, { url: 'https://example.com' }, { success: true, truncated: true, content: 'partial' }],
  ])('states the cut on a %s row', (toolName, args, result) => {
    expect(presentationOf(toolName, args, result).truncated).toBe(true)
  })
})

describe('zcodeToolCallIR bash timeouts', () => {
  // A timed-out Bash says the call was STOPPED, not failed. The payload states it
  // through `statusOverride`, which is the declared route for an outcome the
  // envelope cannot see.
  it('reads a timed-out command as cancelled', () => {
    const call = presentationOf(ZCODE_TOOL.Bash, { command: 'sleep 90' }, {
      success: true,
      content: 'partial',
      perf: { detail: { kind: 'command', command: { timedOut: true } } },
    })
    expect(call.kind).toBe('execute')
    expect(call.status).toBe('cancelled')
  })
})

function factsOf(row: ZCodeRow, parsed?: ParsedMessageContent) {
  return zcodeToolFacts(row, zcodeExtractTool(row.parsed)!, parsed)
}

/**
 * One kind ZCode reads differently from the shared table, and the row that proves it.
 *
 * Each case states four things: the arguments, the request ZCode answers, and the
 * request `DEFAULT_TOOL_REQUESTS` answers from the SAME arguments. Every case carries
 * a decoy key -- `instructions`, `cmd`, `text`, `taskId`, `id`, `cron` -- that the
 * shared table reads and ZCode does not, or the reverse, so the two answers differ at
 * a key rather than only at a value.
 *
 * The comparison is the point. No TYPE can refuse an override that reads the arguments
 * alone: such a function satisfies a slot that supplies the arguments and the facts,
 * so an override quietly replaced by the shared entry still compiles. Only this
 * assertion catches it.
 */
const OVERRIDE_CASES: ReadonlyArray<readonly [ToolKind, string, Record<string, unknown>, Record<string, unknown>, unknown, unknown]> = [
  [
    'agent',
    ZCODE_TOOL.Agent,
    { description: 'Inspect the parser', subagent_type: 'reviewer', prompt: 'Read it', instructions: 'Never read' },
    { success: true, content: '' },
    { description: 'Inspect the parser', agentType: 'reviewer', prompt: 'Read it' },
    { description: 'Inspect the parser', prompt: 'Read it' },
  ],
  [
    'edit',
    ZCODE_TOOL.Edit,
    { file_path: '/project/a.ts', old_string: 'before', new_string: 'after' },
    { success: true, content: 'Applied' },
    { changes: [{ filePath: '/project/a.ts', structuredPatch: null, oldStr: 'before', newStr: 'after' }], replaceAll: undefined },
    // The shared entry reads the same file and the same two sides, and it states the
    // operation beside them. The ZCode entry reads `replace_all` as well, and it takes
    // the landed diff from the facts, which the arguments alone cannot supply.
    { changes: [{ filePath: '/project/a.ts', operation: 'edit', structuredPatch: null, oldStr: 'before', newStr: 'after' }] },
  ],
  [
    'write',
    ZCODE_TOOL.Write,
    { file_path: '/project/b.ts', content: 'hello' },
    { success: true, content: 'Wrote' },
    { changes: [{ filePath: '/project/b.ts', structuredPatch: null, oldStr: '', newStr: 'hello', operation: 'add' }], replaceAll: undefined },
    // The shared entry states the file and the addition, and no BODY: `content` is a
    // best-effort reading that each provider spells for itself.
    { changes: [{ filePath: '/project/b.ts', operation: 'add', structuredPatch: null, oldStr: '', newStr: '' }] },
  ],
  [
    'execute',
    ZCODE_TOOL.Js,
    { command: 'draw()', description: 'Draw a chart', cmd: 'never read' },
    { success: true, content: 'ok' },
    { command: 'draw()', language: 'javascript', description: 'Draw a chart' },
    { command: 'draw()', description: 'Draw a chart' },
  ],
  [
    'mcp',
    'mcp__other__thing',
    { query: 'renderer' },
    { success: true, content: 'The tool response', display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' } },
    { server: 'docs', tool: 'lookup', args: { query: 'renderer' } },
    { args: { query: 'renderer' }, server: '', tool: '' },
  ],
  [
    'message',
    ZCODE_TOOL.SendMessage,
    { recipient: 'peer-1', message: 'Ship it', text: 'never read' },
    { success: true, content: 'Message sent' },
    { to: 'peer-1', text: 'Ship it' },
    { to: undefined, text: 'never read', summary: undefined },
  ],
  [
    'question',
    ZCODE_TOOL.AskUserQuestion,
    { questions: [{ header: 'Parser', question: 'Which parser should I write?', options: [{ label: 'PEG', value: 'PEG' }] }] },
    { success: true, content: 'PEG' },
    { questions: [{ header: 'Parser', question: 'Which parser should I write?', options: [{ label: 'PEG', description: undefined, preview: undefined }] }] },
    { questions: [] },
  ],
  [
    'task',
    ZCODE_TOOL.TaskOutput,
    { task_id: 't-1', taskId: 'never read' },
    { success: true, display: { kind: 'task_stop', taskId: 'task-42' } },
    { action: 'stop', taskId: 't-1' },
    { action: 'other', taskId: 't-1' },
  ],
  [
    'todo',
    ZCODE_TOOL.TodoWrite,
    { todos: [{ content: 'Write the parser', status: 'pending' }] },
    { success: true },
    { items: [expect.objectContaining({ content: 'Write the parser', status: 'pending' })] },
    { items: [] },
  ],
  [
    'trigger',
    ZCODE_TOOL.CronCreate,
    { trigger_id: 't-1', id: 'never read', name: 'Nightly', schedule: '0 0 * * *', cron: 'never read' },
    { success: true, content: 'Created' },
    { action: 'create', triggerId: 't-1', name: 'Nightly', schedule: '0 0 * * *' },
    // The ACTION is the whole deviation. The id, the label and the schedule come from
    // the shared entry, which reads the same five spellings, so the two answers differ
    // on that one field alone -- the tool name states it and no argument carries it.
    { action: 'other', triggerId: 't-1', name: 'Nightly', schedule: '0 0 * * *' },
  ],
]

describe('ZCODE_TOOL_REQUEST_OVERRIDES', () => {
  it('deviates on exactly the kinds a ZCode fact fills', () => {
    expect(Object.keys(ZCODE_TOOL_REQUEST_OVERRIDES).sort()).toEqual([
      'agent',
      'edit',
      'execute',
      'mcp',
      'message',
      'question',
      'task',
      'todo',
      'trigger',
      'write',
    ])
  })

  it('states one case for every key it deviates on', () => {
    expect(OVERRIDE_CASES.map(([kind]) => kind).sort()).toEqual(Object.keys(ZCODE_TOOL_REQUEST_OVERRIDES).sort())
  })

  it.each(OVERRIDE_CASES)('reads a %s request from the keys ZCode spells', (kind, toolName, args, result, request) => {
    const call = presentationOf(toolName, args, result)
    expect(call.kind).toBe(kind)
    expect(call.request).toEqual(request)
  })

  it.each(OVERRIDE_CASES)('answers a %s request the shared table cannot', (kind, _toolName, args, _result, request, shared) => {
    expect(DEFAULT_TOOL_REQUESTS[kind](args)).toEqual(shared)
    expect(request).not.toEqual(shared)
  })

  // The display's own pair wins over the wire name, which states a different server
  // and a different tool for the same call.
  it('reads an MCP identity from the result display ahead of the wire name', () => {
    const call = presentationOf('mcp__other__thing', {}, {
      success: true,
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' },
    })
    expect(call.kind === 'mcp' ? [call.request.server, call.request.tool] : []).toEqual(['docs', 'lookup'])
  })

  // The wire name answers when no display states a pair.
  it('reads an MCP identity from the wire name when the display states none', () => {
    const call = presentationOf('mcp__other__thing', {}, { success: true, content: 'done' })
    expect(call.kind === 'mcp' ? [call.request.server, call.request.tool] : []).toEqual(['other', 'thing'])
  })

  // The display's `input` STRING is the source, not the arguments object: it keeps an
  // integer too large for a JavaScript number exactly as the app-server sent it.
  it('pretty-prints the argument text the display states, not the arguments object', () => {
    const call = presentationOf('mcp__docs__lookup', { query: 'renderer' }, {
      success: true,
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup', input: '{"query":"renderer","limit":9007199254740993}' },
    })
    expect(call.kind === 'mcp' ? call.request.argsText : undefined).toContain('9007199254740993')
  })

  // The words the RESULT states fill a message the arguments left empty.
  it('fills a message from the result text when the arguments carry none', () => {
    const call = presentationOf(ZCODE_TOOL.SendMessage, { to: 'peer-1' }, { success: true, content: 'Ship it' })
    expect(call.kind === 'message' ? call.request : undefined).toEqual({ to: 'peer-1', text: 'Ship it' })
  })

  // `to` is ZCode's first spelling and `recipient` its second.
  it('reads a message recipient from to ahead of recipient', () => {
    const call = presentationOf(ZCODE_TOOL.SendMessage, { to: 'peer-1', recipient: 'never read', message: 'Ship it' }, { success: true })
    expect(call.kind === 'message' ? call.request.to : undefined).toBe('peer-1')
  })

  // The three sandbox tools state the language; every other execute tool states none.
  it.each([ZCODE_TOOL.Js, ZCODE_TOOL.JsAddNodeModuleDir, ZCODE_TOOL.JsReset])('states the sandbox language for %s', (toolName) => {
    const call = presentationOf(toolName, { command: 'draw()' }, { success: true, content: 'ok' })
    expect(call.kind === 'execute' ? call.request.language : undefined).toBe('javascript')
  })

  it('states no language for a shell command', () => {
    const call = presentationOf(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: 'a.ts' })
    expect(call.kind === 'execute' ? call.request.language : undefined).toBeUndefined()
  })

  // The hint decides the action, and the two hints answer differently.
  it.each([
    ['task_stop', 'stop'],
    ['task_output', 'output'],
  ])('reads the %s display as the %s action', (hint, action) => {
    const call = presentationOf(ZCODE_TOOL.TaskOutput, {}, { success: true, display: { kind: hint, taskId: 'task-42' } })
    expect(call.kind === 'task' ? call.request.action : undefined).toBe(action)
  })

  it('reads a task call that states no display as neither action', () => {
    const call = presentationOf(ZCODE_TOOL.TaskOutput, {}, { success: true, content: 'done' })
    expect(call.kind === 'task' ? call.request.action : undefined).toBe('other')
  })

  // The ARGUMENTS answer first, so a later release that adds the key still wins.
  it('reads a trigger action from the arguments ahead of the tool name', () => {
    const call = presentationOf(ZCODE_TOOL.CronCreate, { action: 'run', trigger_id: 't-1' }, { success: true, content: 'Ran' })
    expect(call.kind === 'trigger' ? call.request.action : undefined).toBe('run')
  })

  // An action word the request does not declare falls back to the tool's own.
  it('refuses an action word the request does not declare', () => {
    const call = presentationOf(ZCODE_TOOL.CronList, { action: 'frobnicate' }, { success: true, content: 'done' })
    expect(call.kind === 'trigger' ? call.request.action : undefined).toBe('list')
  })
})

describe('ZCODE_TOOL_READERS', () => {
  it('states one reader for every tool kind', () => {
    expect(Object.keys(ZCODE_TOOL_READERS).sort()).toEqual([...TOOL_KINDS].sort())
  })

  // Every reader runs against a row of a different kind and answers its OWN kind. A
  // reader that reaches for a fact this row does not carry throws here rather than in
  // the transcript, where the error boundary replaces the whole message.
  it('answers each kind at the key that states it', () => {
    const facts = factsOf(resultRow('SomeToolAddedLater', { path: '/project/a.ts' }, { success: true, content: 'done' }))
    for (const kind of TOOL_KINDS)
      expect(ZCODE_TOOL_READERS[kind](facts).kind).toBe(kind)
  })

  // The eleven kinds no ZCode tool takes still fill their declared request, so a row
  // that ever reaches one draws the kind's card rather than throwing inside a renderer
  // that reads `request.changes[0]` or `request.path` with no guard.
  it.each(['agents', 'chart', 'delete', 'image', 'list', 'memory', 'move', 'report', 'search', 'think', 'wait'] as const)('fills the declared request of %s', (kind) => {
    const facts = factsOf(resultRow('SomeToolAddedLater', { path: '/project/a.ts', pattern: '*.ts' }, { success: true, content: 'done' }))
    expect(ZCODE_TOOL_READERS[kind](facts).request).toEqual(DEFAULT_TOOL_REQUESTS[kind](facts.input))
  })
})

describe('zcodeReclassify', () => {
  // A to-do tool whose input carries no list states no checklist to draw.
  it('demotes a to-do call that carries no list', () => {
    expect(zcodeReclassify(factsOf(resultRow(ZCODE_TOOL.TodoWrite, {}, { success: true, content: 'Saved' })))).toBe('other')
  })

  it('keeps a to-do call that carries a list', () => {
    const row = resultRow(ZCODE_TOOL.TodoWrite, { todos: [{ content: 'Write the parser', status: 'pending' }] }, { success: true })
    expect(zcodeReclassify(factsOf(row))).toBe('todo')
  })

  // A row with no tool name has no label to tell it apart from an uncategorized one.
  it('folds a row that states no tool name onto the generic card', () => {
    const noName = row(event(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'recovered output' } }))
    expect(zcodeToolKind(noName.toolName)).toBe('')
    expect(zcodeReclassify(factsOf(noName))).toBe('other')
  })

  it.each([
    [ZCODE_TOOL.Bash, 'execute'],
    [ZCODE_TOOL.Read, 'read'],
    [ZCODE_TOOL.Glob, 'glob'],
    [ZCODE_TOOL.CronList, 'trigger'],
  ] as const)('swaps no kind for %s', (toolName, kind) => {
    expect(zcodeReclassify(factsOf(resultRow(toolName, {}, { success: true, content: 'done' })))).toBe(kind)
  })
})

// A failed file change keeps the file it asked to change. `RequestedChangesBody` is
// the one place that decides whether a failed row draws its diff, and it refuses -- so
// emptying the request removed nothing from the body and took the FILE NAME out of the
// row's title instead.
describe('a failed ZCode file change', () => {
  it.each([
    [ZCODE_TOOL.Edit, { file_path: '/project/a.ts', old_string: 'before', new_string: 'after' }],
    [ZCODE_TOOL.Write, { file_path: '/project/a.ts', content: 'hello' }],
  ])('keeps the file %s asked to change', (toolName, args) => {
    const call = presentationOf(toolName, args, { success: false, content: 'Permission denied' })
    expect(call.status).toBe('failed')
    expect(call.result).toEqual({ failure: true, text: 'Permission denied' })
    const changes = call.kind === 'edit' || call.kind === 'write' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/a.ts'])
  })

  // The path the arguments omit still reaches the request, through the display.
  it('recovers the file from the result display when the arguments state none', () => {
    const call = presentationOf(ZCODE_TOOL.Edit, { old_string: 'before', new_string: 'after' }, {
      success: false,
      content: 'Permission denied',
      display: { kind: 'file_diff', filePath: '/project/a.ts' },
    })
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/a.ts'])
  })
})

// `ApplyPatch` states its whole change in ONE text argument, and no file-path key sits
// beside it. A request that reads the key list alone states no file, so the row draws
// the word "Edit" and no file name -- at every state of the call, not only a failed one.
describe('a ZCode ApplyPatch call', () => {
  const PATCH = '*** Begin Patch\n*** Update File: /project/a.ts\n@@\n-before\n+after\n*** End Patch'
  const MULTI_FILE_PATCH = '*** Begin Patch\n*** Update File: /project/a.ts\n@@\n-before\n+after\n*** Delete File: /project/b.ts\n*** End Patch'

  /** The changes the call reports as LANDED, or null for a result that states none. */
  function landedChanges(call: ToolCallIR): FileEditDiff[] | null {
    return call.kind === 'edit' && call.result && 'changes' in call.result ? call.result.changes : null
  }

  it.each([
    ['a finished call', { success: true, content: 'Applied' }],
    ['a failed call', { success: false, content: 'Permission denied' }],
  ])('names the file the patch changes on %s', (_name, result) => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: PATCH }, result)
    expect(call.kind).toBe('edit')
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/a.ts'])
  })

  // The opening frame carries the same arguments and no result at all, which is the
  // state the reader watches for longest.
  it('names the file before any result lands', () => {
    const call = zcodeToolCallIR(row(event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.ApplyPatch, input: { patch: PATCH } }), ZCODE_TOOL.ApplyPatch))!
    expect(call.result).toBeUndefined()
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/a.ts'])
  })

  // One patch can carry several files, which is what puts the reading ahead of the
  // result display: that display states at most one.
  it('names every file one patch changes', () => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: MULTI_FILE_PATCH }, { success: true, content: 'Applied' })
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/a.ts', '/project/b.ts'])
    expect(changes.map(change => change.operation)).toEqual(['edit', 'delete'])
  })

  // The result half reads the same patch. A finished call draws the RESULT alone --
  // `RequestedChangesBody` stops drawing the request the moment one lands -- so a
  // result that states no change leaves the row with the file name and the word
  // "Applied" over nothing.
  it('draws the diff the patch states once the call finished', () => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: PATCH }, { success: true, content: 'Applied' })
    expect(landedChanges(call)).toStrictEqual([{
      filePath: '/project/a.ts',
      operation: 'edit',
      showLineNumbers: false,
      structuredPatch: [{ oldStart: 0, oldLines: 1, newStart: 0, newLines: 1, lines: ['-before', '+after'] }],
    }])
  })

  // EVERY file of the patch, for the reason the request half states: the result
  // display carries at most one, and a row that draws that one alone reports a third
  // of a three-file change.
  it('draws every file of a multi-file patch once the call finished', () => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: MULTI_FILE_PATCH }, { success: true, content: 'Applied' })
    expect(landedChanges(call)).toStrictEqual([
      {
        filePath: '/project/a.ts',
        operation: 'edit',
        showLineNumbers: false,
        structuredPatch: [{ oldStart: 0, oldLines: 1, newStart: 0, newLines: 1, lines: ['-before', '+after'] }],
      },
      { filePath: '/project/b.ts', operation: 'delete', showLineNumbers: false, structuredPatch: null, oldStr: '', newStr: '' },
    ])
  })

  // A patch this build cannot read names no file, and a file change that names none
  // is not one: the IR refuses the pair rather than heading a row with the word
  // "Edit" and nothing else. The uncategorized card takes it, where the PATCH stays
  // visible as the argument the call was made with.
  it('takes the uncategorized card for a patch the shared reader refuses', () => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: 'not a patch' }, { success: true, content: 'Applied' })
    expect(call.kind).toBe('other')
    expect(call.name).toBe(ZCODE_TOOL.ApplyPatch)
    expect(call.kind === 'other' ? call.request.args : null).toEqual({ patch: 'not a patch' })
  })

  // The result half of the same refusal: the words the tool printed, and no diff.
  it('keeps the words of a patch the shared reader refuses', () => {
    const call = presentationOf(ZCODE_TOOL.ApplyPatch, { patch: 'not a patch' }, { success: true, content: 'Applied' })
    expect(landedChanges(call)).toBeNull()
    expect(call.kind === 'other' ? typedResult(call)?.content : null).toEqual([{ type: 'text', text: 'Applied' }])
  })

  // Only `ApplyPatch` sends a patch. Another tool's `patch` argument would be that
  // tool's own text, and the landed diff stays the answer for the two that have one.
  it('keeps the landed diff of an Edit that also carries a patch argument', () => {
    const call = presentationOf(ZCODE_TOOL.Edit, { file_path: '/project/c.ts', old_string: 'before', new_string: 'after', patch: PATCH }, { success: true, content: 'Edited' })
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes.map(change => change.filePath)).toEqual(['/project/c.ts'])
  })

  // The same order on the result half. The display states the hunk the file really
  // took, line numbers and all, and the `patch` argument of a tool that sends no
  // apply-patch envelope never displaces it.
  it('draws the landed diff of an Edit that also carries a patch argument', () => {
    const call = presentationOf(ZCODE_TOOL.Edit, { file_path: '/project/c.ts', old_string: 'before', new_string: 'after', patch: PATCH }, {
      success: true,
      content: 'Edited',
      display: {
        kind: ZCODE_DISPLAY.FileDiff,
        filePath: '/project/c.ts',
        structuredPatch: [{ oldStart: 12, oldLines: 1, newStart: 12, newLines: 1, lines: ['-before', '+after'] }],
      },
    })
    expect(landedChanges(call)).toStrictEqual([{
      filePath: '/project/c.ts',
      structuredPatch: [{ oldStart: 12, oldLines: 1, newStart: 12, newLines: 1, lines: ['-before', '+after'] }],
    }])
  })
})

// The four cron tools ARE the four actions; no `action` argument sits beside them.
describe('a ZCode cron call', () => {
  it.each([
    [ZCODE_TOOL.CronCreate, 'create'],
    [ZCODE_TOOL.CronList, 'list'],
    [ZCODE_TOOL.CronUpdate, 'update'],
    [ZCODE_TOOL.CronDelete, 'delete'],
  ] as const)('reads the action %s states', (toolName, action) => {
    const call = presentationOf(toolName, { trigger_id: 't-1' }, { success: true, content: 'done' })
    expect(call.kind).toBe('trigger')
    expect(call.kind === 'trigger' ? call.request.action : undefined).toBe(action)
  })
})

// The tool name is the constant word `Skill` on this path, so the old fallback claimed
// every unnamed call ran a skill called "Skill".
describe('a ZCode skill call', () => {
  it('names no skill when the arguments state none', () => {
    const call = presentationOf(ZCODE_TOOL.Skill, {}, { success: true, content: 'done' })
    expect(call.kind).toBe('skill')
    expect(call.kind === 'skill' ? call.request.name : 'unread').toBeUndefined()
  })

  it('names the skill the arguments state', () => {
    const call = presentationOf(ZCODE_TOOL.Skill, { skill: 'create-pr' }, { success: true, content: 'done' })
    expect(call.kind === 'skill' ? call.request.name : undefined).toBe('create-pr')
  })
})

// The kind inventory, derived rather than declared. ZCode reaches a kind from three
// sources -- the name table, an MCP wire name, and a result display hint -- so the set
// it produces cannot be read off any one of them. A new tool mapping that reaches a
// kind `ZCODE_TOOL_READERS` fills with the shared request alone lands here first.
describe('the tool kinds ZCode produces', () => {
  // One argument record for every tool, so each name reaches its own kind. The FILE
  // is stated for the same reason the checklist and the question are: the IR refuses
  // an `edit` or a `write` whose request names no file and degrades it to the
  // uncategorized row, which would drop both kinds out of the set this walk measures.
  const ARGS = {
    todos: [{ content: 'Write the parser', status: 'pending' }],
    questions: [{ question: 'Which parser?' }],
    file_path: '/project/a.ts',
  }
  const PRODUCED: readonly ToolKind[] = [
    'agent',
    'edit',
    'execute',
    'fetch',
    'glob',
    'grep',
    'mcp',
    'message',
    'other',
    'question',
    'read',
    'skill',
    'switch_mode',
    'task',
    'todo',
    'trigger',
    'web_search',
    'write',
  ]

  function producedKinds(): Set<ToolKind> {
    const kinds = new Set<ToolKind>()
    for (const toolName of Object.values(ZCODE_TOOL))
      kinds.add(presentationOf(toolName, ARGS, { success: true, content: 'done' }).kind)
    // An MCP wire name, which no entry of the name table spells.
    kinds.add(presentationOf('mcp__docs__lookup', ARGS, { success: true, content: 'done' }).kind)
    // Every display hint, over a tool name the table does not know.
    for (const hint of Object.values(ZCODE_DISPLAY))
      kinds.add(presentationOf('SomeToolAddedLater', ARGS, { success: true, content: 'done', display: { kind: hint } }).kind)
    // A row that states no tool name at all.
    kinds.add(zcodeToolCallIR(row(event(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'done' } })))!.kind)
    return kinds
  }

  it('reaches eighteen of the thirty shared kinds', () => {
    expect([...producedKinds()].sort()).toEqual([...PRODUCED].sort())
  })

  // The complement, spelled out: these are the twelve `zcodeArgumentsOnly` and the
  // folded empty kind answer for. `''` is here because `zcodeReclassify` folds it.
  it('reaches none of the twelve kinds no ZCode tool takes', () => {
    const produced = producedKinds()
    expect(TOOL_KINDS.filter(kind => !produced.has(kind))).toEqual([
      '',
      'agents',
      'chart',
      'delete',
      'image',
      'list',
      'memory',
      'move',
      'report',
      'search',
      'think',
      'wait',
    ])
  })
})
