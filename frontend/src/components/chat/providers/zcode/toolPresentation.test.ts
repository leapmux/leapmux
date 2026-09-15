import type { ZCodeRow } from './extractors/toolCommon'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolPresentationMeta } from '../../results/toolResultMeta'
import { input } from '../testUtils'
import { zcodeRow } from './extractors/toolCommon'
import { ZCODE_WEB_FETCH } from './protocol'
import { zcodeToolKind, zcodeToolMessageSource, zcodeToolPresentation } from './toolPresentation'

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
  return zcodeToolPresentation(resultRow(toolName, args, result))!
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
    [ZCODE_WEB_FETCH, 'fetch'],
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

describe('zcodeToolPresentation bodies', () => {
  it('maps an MCP display to the shared rich-content body and recovers its arguments', () => {
    const presentation = presentationOf('mcp__docs__lookup', { query: 'renderer' }, {
      success: true,
      content: 'The tool response',
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' },
    })
    expect(presentation.body.type).toBe('mcp')
    if (presentation.body.type !== 'mcp')
      throw new Error('expected an MCP body')
    expect(presentation.body.source.server).toBe('docs')
    expect(presentation.body.source.argsJson).toContain('renderer')
  })

  it('maps a task-stop display to the shared status body', () => {
    const presentation = presentationOf(ZCODE_TOOL.TaskOutput, {}, {
      success: true,
      display: { kind: 'task_stop', taskId: 'task-42', command: 'npm run dev', message: 'Stopped the task' },
    })
    expect(presentation.body).toEqual({
      type: 'status',
      source: { title: 'Stopped task task-42', outcome: 'stopped', command: 'npm run dev', output: 'Stopped the task' },
    })
  })

  it('maps a failed message display to the failed status outcome', () => {
    const presentation = presentationOf('SendMessage', {}, {
      success: true,
      display: { kind: 'local_agent_message', status: 'failed', error: 'Peer unavailable' },
    })
    expect(presentation.body.type).toBe('status')
    if (presentation.body.type !== 'status')
      throw new Error('expected a status body')
    expect(presentation.body.source.outcome).toBe('failed')
    expect(presentation.body.source.title).toBe('Failed')
  })

  // An image display carries its pictures beside the text, which the shared image list
  // draws. The body therefore stays the plain text of the result.
  it('keeps a node-image display as text and reports its images separately', () => {
    const parsed = event(ZCODE_TOOL_KIND.Result, {
      result: { success: true, content: 'Rendered the chart', display: { kind: 'node_repl_images', images: [{ base64: 'AAAA', mimeType: 'image/png' }] } },
    })
    const source = zcodeToolMessageSource(row(parsed, 'js'))!
    expect(source.presentation.body).toEqual({ type: 'text' })
    expect(source.presentation.output).toBe('Rendered the chart')
    expect(source.images).toHaveLength(1)
  })

  it('copies a numbered read without the native line-number prefixes', () => {
    const presentation = presentationOf(ZCODE_TOOL.Read, { file_path: '/project/a.ts' }, { success: true, content: '1\tfirst\n2\tsecond' })
    expect(presentation.body.type).toBe('read')
    expect(toolPresentationMeta(presentation).copyableContent()).toBe('first\nsecond')
  })

  it('states the failure text of a failed call', () => {
    const presentation = presentationOf(ZCODE_TOOL.Read, { file_path: '/project/missing.ts' }, { success: false, content: 'File does not exist' })
    expect(presentation.body).toEqual({ type: 'text' })
    expect(presentation.output).toBe('File does not exist')
  })

  it('draws the checklist of an open TodoWrite request', () => {
    const parsed = event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.TodoWrite, input: { todos: [{ content: 'Write the parser', status: 'pending' }] } })
    const presentation = zcodeToolPresentation(row(parsed))!
    expect(presentation.kind).toBe('todo')
    expect(presentation.title).toBe('1 task')
    expect(presentation.body).toMatchObject({ type: 'todo' })
  })
})

describe('zcodeToolPresentation truncation', () => {
  it('leaves the notice to the row for a body that cannot state it', () => {
    const presentation = presentationOf(ZCODE_TOOL.Edit, {}, {
      success: true,
      truncated: true,
      display: { kind: 'file_diff', filePath: '/project/a.ts', structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }] },
    })
    expect(presentation.truncated).toBe(true)
  })

  // The command body draws its own notice, so a second flag on the row would print
  // `Output truncated` twice under one result.
  it('gives the flag to the command body, not to the row', () => {
    const presentation = presentationOf(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, truncated: true, content: 'partial' })
    expect(presentation.truncated).toBeUndefined()
    expect(presentation.body).toMatchObject({ type: 'command', source: { truncated: true } })
  })

  // The rich-content body replaces the whole row with the shared card, which draws no
  // part of the presentation around it.
  it('keeps no flag for a rich-content body, which no notice can reach', () => {
    const presentation = presentationOf('mcp__docs__lookup', {}, {
      success: true,
      truncated: true,
      content: 'The tool response',
      display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' },
    })
    expect(presentation.truncated).toBeUndefined()
  })

  it('gives the flag to the search body, not to the row', () => {
    const presentation = presentationOf(ZCODE_TOOL.Glob, { pattern: '*.ts' }, { success: true, truncated: true, content: 'a.ts' })
    expect(presentation.truncated).toBeUndefined()
    expect(presentation.body).toMatchObject({ type: 'search', source: { truncated: true } })
  })
})

describe('zcodeToolMessageSource', () => {
  it('reads a scheduled row as the request of its span', () => {
    const source = zcodeToolMessageSource(row(event(ZCODE_TOOL_KIND.Scheduled, { toolName: ZCODE_TOOL.Bash, input: { command: 'ls' } })))!
    expect(source).toMatchObject({ id: 'call', role: 'request', status: 'in_progress' })
    expect(source.presentation.label).toBe(ZCODE_TOOL.Bash)
  })

  it('reads a result row as the end of its span', () => {
    const source = zcodeToolMessageSource(resultRow(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: 'a.ts' }))!
    expect(source).toMatchObject({ role: 'result', status: 'completed' })
  })

  it('reports a failed call', () => {
    const source = zcodeToolMessageSource(resultRow(ZCODE_TOOL.Read, {}, { success: false, content: 'gone' }))!
    expect(source.status).toBe('failed')
  })

  // A turn that ends while the call runs stores the agent's own last frame, which
  // still reads as a call in progress.
  it('reads a retained frame as a cancelled result', () => {
    const parsed: ParsedMessageContent = { ...input(event(ZCODE_TOOL_KIND.Progress, { stdoutTail: 'partial output' })), completion: MessageCompletion.INTERRUPTED }
    const source = zcodeToolMessageSource(row(parsed.parentObject!, ZCODE_TOOL.Bash), parsed)!
    expect(source).toMatchObject({ role: 'result', status: 'cancelled' })
    expect(source.presentation.body).toMatchObject({ type: 'command' })
  })

  it('invents no tool name for a row that states none', () => {
    const source = zcodeToolMessageSource(row(event(ZCODE_TOOL_KIND.Result, { result: { success: true, content: 'recovered output' } })))!
    expect(source.presentation.kind).toBe('')
    expect(source.presentation.label).toBeUndefined()
  })
})

// `TaskOutput` reads a background task from the runtime, and no shared kind states
// that -- so the kind table maps it nowhere and the row falls to `other`, whose
// icon is the wrench every unrecognized tool draws. Its own icon is what lets a
// reader pick the background-task rows out of a transcript.
describe('the icon of a tool no shared kind fits', () => {
  it('keeps its own icon for TaskOutput', () => {
    const presentation = presentationOf(ZCODE_TOOL.TaskOutput, {}, { success: true, content: 'done' })
    expect(presentation.kind).toBe('other')
    expect(presentation.icon).toBeDefined()
  })

  it('leaves every kind the table maps to the shared icon', () => {
    const presentation = presentationOf(ZCODE_TOOL.Bash, { command: 'ls' }, { success: true, content: '' })
    expect(presentation.kind).toBe('execute')
    expect(presentation.icon).toBeUndefined()
  })
})
