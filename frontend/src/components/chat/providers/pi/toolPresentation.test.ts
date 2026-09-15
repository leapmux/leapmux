import type { PiToolRow } from './toolPresentation'
import { describe, expect, it } from 'vitest'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolPresentationMeta } from '../../results/toolResultMeta'
import { input } from '../testUtils'
import { PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from './protocol'
import { piToolMessageSource, piToolPresentation, piToolRow } from './toolPresentation'

const text = (value: string) => [{ type: 'text', text: value }]

function start(toolName: string, args: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: 'tool_execution_start', toolCallId: 'call', toolName, args }
}

function end(toolName: string, result: Record<string, unknown>, isError = false): Record<string, unknown> {
  return { type: 'tool_execution_end', toolCallId: 'call', toolName, result, isError }
}

function resultRow(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>, isError = false): PiToolRow {
  return piToolRow(end(toolName, result, isError), input(start(toolName, args)), undefined)!
}

function requestRow(toolName: string, args: Record<string, unknown> = {}): PiToolRow {
  return piToolRow(start(toolName, args), undefined, undefined)!
}

describe('piToolRow', () => {
  it('refuses a payload that is not a tool event', () => {
    expect(piToolRow({ type: 'message_end' }, undefined, undefined)).toBeNull()
  })

  it('reads a completion event as the end of its call', () => {
    expect(resultRow(PI_TOOL.Read, {}, { content: text('body') }).finished).toBe(true)
  })

  // A turn that ended while the call ran stores the start frame again as the closing
  // row, with whatever partial result Pi did report beside it.
  it('reads a retained start frame as the end of its call', () => {
    const row = piToolRow(start(PI_TOOL.Bash, { command: 'ls' }), undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(row.finished).toBe(true)
  })

  // Pi reports a refused to-do operation in `details.error` and still flags the call a
  // success, so the row's own outcome must read that field.
  it('reports a to-do failure that Pi did not flag', () => {
    const row = resultRow(PI_TOOL.Todo, { action: 'update', id: 99 }, {
      content: text('Error: #99 not found'),
      details: { action: 'update', params: { action: 'update', id: 99 }, error: '#99 not found', tasks: [] },
    })
    expect(row.tool.isError).toBe(false)
    expect(row.isError).toBe(true)
  })
})

describe('piToolPresentation kinds and labels', () => {
  it.each([
    [PI_TOOL.Bash, 'execute', 'Bash'],
    [PI_POWERSHELL_TOOL, 'execute', 'PowerShell'],
    [PI_TOOL.Read, 'read', 'Read'],
    [PI_TOOL.Write, 'write', 'Write'],
    [PI_TOOL.Edit, 'edit', 'Edit'],
    [PI_SEARCH_TOOL.Grep, 'grep', PI_SEARCH_TOOL.Grep],
    [PI_SEARCH_TOOL.Find, 'glob', PI_SEARCH_TOOL.Find],
    [PI_SEARCH_TOOL.List, 'list', PI_SEARCH_TOOL.List],
    [PI_TOOL.Todo, 'todo', PI_TOOL.Todo],
  ] as const)('maps %s to the %s kind', (toolName, kind, label) => {
    const presentation = piToolPresentation(requestRow(toolName))
    expect(presentation.kind).toBe(kind)
    expect(presentation.label).toBe(label)
  })

  // A plain object answers `toString` from its prototype, which would give the row a
  // function where a label belongs.
  it.each(['constructor', 'toString', '__proto__'])('states the name of an extension called %s', (toolName) => {
    const presentation = piToolPresentation(requestRow(toolName, { query: 'marker' }))
    expect(presentation.kind).toBe('')
    expect(presentation.label).toBe(toolName)
  })

  it('states the PowerShell language so the row highlights the command', () => {
    expect(piToolPresentation(requestRow(PI_POWERSHELL_TOOL, { command: 'Get-ChildItem' })).commandLanguage).toBe('powershell')
    expect(piToolPresentation(requestRow(PI_TOOL.Bash, { command: 'ls' })).commandLanguage).toBeUndefined()
  })

  // The shared header draws the command itself, so a title here would put the tool's
  // own name above the very command it ran.
  it('leaves a command row without a title of its own', () => {
    expect(piToolPresentation(requestRow(PI_TOOL.Bash, { command: 'ls' })).title).toBe('')
  })
})

describe('piToolPresentation edit arguments', () => {
  it('states a single substitution as the pair the shared title reads', () => {
    const presentation = piToolPresentation(requestRow(PI_TOOL.Edit, { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] }))
    expect(presentation.input).toMatchObject({ oldText: 'before', newText: 'after' })
    expect(presentation.inputText).toBeUndefined()
  })

  it('states the size of a multi-substitution edit', () => {
    const presentation = piToolPresentation(requestRow(PI_TOOL.Edit, {
      path: '/project/a.ts',
      edits: [{ oldText: 'a', newText: 'b' }, { oldText: 'c', newText: 'd' }],
    }))
    expect(presentation.inputText).toBe('2 edits')
    expect(presentation.input.oldText).toBeUndefined()
  })
})

describe('piToolPresentation bodies', () => {
  it('draws a command result through the shared command body', () => {
    const presentation = piToolPresentation(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))
    expect(presentation.body).toMatchObject({ type: 'command', source: { output: 'a.ts' } })
  })

  it('draws a directory listing through the shared directory body', () => {
    const presentation = piToolPresentation(resultRow(PI_SEARCH_TOOL.List, { path: '/project' }, { content: text('a.ts\nsrc/') }))
    expect(presentation.body).toMatchObject({ type: 'directory', source: { entries: [{ path: 'a.ts' }, { path: 'src/' }] } })
  })

  it('draws a grep result through the shared search body', () => {
    const presentation = piToolPresentation(resultRow(PI_SEARCH_TOOL.Grep, { pattern: 'answer' }, { content: text('a.ts:3: answer') }))
    expect(presentation.body).toMatchObject({ type: 'search', source: { variant: 'search', content: 'a.ts:3: answer', numLines: 1 } })
  })

  it('draws an unrecognized extension through the shared rich-content body', () => {
    const presentation = piToolPresentation(resultRow('extension_lookup', { query: 'marker' }, { content: text('Extension report'), details: { count: 0 } }))
    expect(presentation.body).toMatchObject({ type: 'mcp', source: { tool: 'extension_lookup' } })
  })

  it('draws the checklist, its empty state and the note about the named task', () => {
    const presentation = piToolPresentation(resultRow(PI_TOOL.Todo, { action: 'get', id: 1 }, {
      content: text('#1 Inspect sample'),
      details: { action: 'get', params: { action: 'get', id: 1 }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending', description: 'Read the entry points.' }] },
    }))
    expect(presentation.body).toMatchObject({ type: 'todo', description: 'Read the entry points.' })
    expect(presentation.metadata).toEqual([{ label: 'Task ID', value: '1' }])
  })

  it('draws a cleared to-do list with its own empty state', () => {
    const presentation = piToolPresentation(resultRow(PI_TOOL.Todo, { action: 'clear' }, {
      content: text('Cleared'),
      details: { action: 'clear', params: { action: 'clear' }, tasks: [] },
    }))
    expect(presentation.body).toMatchObject({ type: 'todo', items: [], emptyText: 'To-do list cleared' })
  })

  // These tools draw DATA, so a failed call has no body of its own and the row states
  // the error text under the shared header instead.
  it.each([PI_TOOL.Read, PI_TOOL.Edit, PI_TOOL.Write, PI_SEARCH_TOOL.Grep, PI_SEARCH_TOOL.Find, PI_SEARCH_TOOL.List])('states the error text of a failed %s', (toolName) => {
    const presentation = piToolPresentation(resultRow(toolName, {}, { content: text('The call refused.') }, true))
    expect(presentation.body).toEqual({ type: 'text' })
    expect(presentation.output).toBe('The call refused.')
  })
})

describe('piToolPresentation copy text', () => {
  // The plan the row DRAWS, not the envelope that holds it: the generic reading used
  // to copy `{"plan":"# ..."}` with every line break escaped.
  it('copies the plan of a completed plan row', () => {
    const plan = '# Welcome plan\n\nRead the sample.'
    const presentation = piToolPresentation(resultRow(PI_TOOL.PlanComplete, { plan }, { content: text('Plan ready for review.'), details: { plan } }))
    const meta = toolPresentationMeta(presentation)
    expect(meta.copyableContent()).toBe(plan)
    expect(meta.collapsible).toBe(false)
  })

  it('copies the result text of a failed plan row, which is what that row draws', () => {
    const presentation = piToolPresentation(resultRow(PI_TOOL.PlanComplete, {}, { content: text('The plan tool refused.'), details: { plan: '# Ignored' } }, true))
    expect(toolPresentationMeta(presentation).copyableContent()).toBe('The plan tool refused.')
  })

  it('copies the raw diff an edit row draws when the diff cannot be parsed', () => {
    const diff = 'A provider diff in an unknown format'
    const presentation = piToolPresentation(resultRow(PI_TOOL.Edit, { path: '/project/a.ts' }, { content: text('Edit completed'), details: { diff } }))
    const meta = toolPresentationMeta(presentation)
    expect(meta.hasDiff).toBe(false)
    expect(meta.copyableContent()).toBe(diff)
  })

  it('copies the error of a refused to-do operation', () => {
    const presentation = piToolPresentation(resultRow(PI_TOOL.Todo, { action: 'update', id: 99 }, {
      content: text('Error: #99 not found'),
      details: { action: 'update', params: { action: 'update', id: 99 }, error: '#99 not found', tasks: [] },
    }))
    expect(toolPresentationMeta(presentation).copyableContent()).toBe('#99 not found')
  })
})

describe('piToolMessageSource', () => {
  it('reads a start event as the request of its span', () => {
    expect(piToolMessageSource(requestRow(PI_TOOL.Bash, { command: 'ls' }))).toMatchObject({ id: 'call', role: 'request', status: 'in_progress' })
  })

  it('reads a completion event as the end of its span', () => {
    expect(piToolMessageSource(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))).toMatchObject({ role: 'result', status: 'completed' })
  })

  it('reports a failed call', () => {
    expect(piToolMessageSource(resultRow(PI_TOOL.Read, {}, { content: text('gone') }, true)).status).toBe('failed')
  })

  it('reports an interrupted call from the completion LeapMux recorded', () => {
    const row = piToolRow(start(PI_TOOL.Bash, { command: 'ls' }), undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(piToolMessageSource(row, MessageCompletion.INTERRUPTED).status).toBe('cancelled')
  })
})
