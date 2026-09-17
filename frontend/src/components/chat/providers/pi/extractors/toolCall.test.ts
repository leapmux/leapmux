import type { ToolCallRow } from '../../../ir/row'
import type { ToolCallIR } from '../../../ir/toolCall'
import type { PiToolRow } from './toolCall'
import { describe, expect, it } from 'vitest'
import { PI_TOOL } from '~/generated/contracts/pi-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolCallRow } from '../../../ir/row'
import { isFailedResult } from '../../../ir/toolCall'
import { TOOL_KINDS } from '../../../ir/toolKind'
import { toolCallMeta } from '../../../results/tools/meta'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { PI_POWERSHELL_TOOL, PI_SEARCH_TOOL } from '../protocol'
import { PI_TOOL_READERS, PI_TOOL_REQUEST_OVERRIDES, piReclassify, piToolCallIR, piToolFacts, piToolRow, piToolRowRole } from './toolCall'

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

/**
 * The smallest arguments a tool must state for its own kind to build.
 *
 * A file change states the FILE it changes. The IR refuses an `edit` or a `write`
 * whose request names none -- the row composes its header from that list at every
 * state of the call -- and degrades such a call to the uncategorized row, so a case
 * that states no file tests the uncategorized card rather than the tool.
 */
const MINIMAL_ARGS: Readonly<Record<string, Record<string, unknown>>> = {
  [PI_TOOL.Edit]: { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] },
  [PI_TOOL.Write]: { path: '/project/new.ts', content: 'export const a = 1\n' },
}

/** The opening frame of one call, with the smallest arguments its kind needs. */
function minimalRequestRow(toolName: string): PiToolRow {
  return requestRow(toolName, MINIMAL_ARGS[toolName] ?? {})
}

/** The mounted row one call sits in, so its toolbar derivation can be read. */
function rowOf(call: ToolCallIR): ToolCallRow {
  return toolCallRow(call, 'result', { request: false, result: false })
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

describe('piToolCall kinds and labels', () => {
  it.each([
    [PI_TOOL.Bash, 'execute', 'Bash'],
    [PI_POWERSHELL_TOOL, 'execute', 'PowerShell'],
    [PI_TOOL.Read, 'read', 'Read'],
    [PI_TOOL.Write, 'write', 'Write'],
    [PI_TOOL.Edit, 'edit', 'Edit'],
    [PI_SEARCH_TOOL.Grep, 'grep', PI_SEARCH_TOOL.Grep],
    [PI_SEARCH_TOOL.Find, 'glob', PI_SEARCH_TOOL.Find],
    [PI_SEARCH_TOOL.List, 'list', PI_SEARCH_TOOL.List],
  ] as const)('maps %s to the %s kind', (toolName, kind, label) => {
    const call = piToolCallIR(minimalRequestRow(toolName))
    expect(call.kind).toBe(kind)
    expect(call.label).toBe(label)
  })

  it('maps a to-do operation to the todo kind', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Todo, { action: 'list' }))
    expect(call.kind).toBe('todo')
    expect(call.label).toBe(PI_TOOL.Todo)
  })

  // The kind is decided ONCE, from the checklist the row resolved. A demotion that
  // also tested `finished` drew the to-do card while the call ran and became a Model
  // Context Protocol card the instant it ended -- one call, two cards.
  it('states one kind on both frames of a to-do call it cannot read', () => {
    const args = { action: 'teleport' }
    const running = piToolCallIR(requestRow(PI_TOOL.Todo, args))
    const ended = piToolCallIR(resultRow(PI_TOOL.Todo, args, { content: text('done') }))
    expect(running.kind).toBe('mcp')
    expect(ended.kind).toBe('mcp')
  })

  // A plain object answers `toString` from its prototype, which would give the row a
  // function where a label belongs.
  it.each(['constructor', 'toString', '__proto__'])('states the name of an extension called %s', (toolName) => {
    const call = piToolCallIR(requestRow(toolName, { query: 'marker' }))
    // An extension is a Model Context Protocol bridge as far as the row is
    // concerned: it draws the shared card, and the kind says so. The empty kind
    // drew a wrench and the word "Tool" above that very card.
    expect(call.kind).toBe('mcp')
    expect(call.label).toBe(toolName)
  })

  it('states the PowerShell language so the row highlights the command', () => {
    const shell = piToolCallIR(requestRow(PI_POWERSHELL_TOOL, { command: 'Get-ChildItem' }))
    const bash = piToolCallIR(requestRow(PI_TOOL.Bash, { command: 'ls' }))
    expect(shell.kind).toBe('execute')
    expect(shell.kind === 'execute' && shell.request.language).toBe('powershell')
    expect(bash.kind === 'execute' && bash.request.language).toBeUndefined()
  })

  // The shared header draws the command itself, so a title here would put the tool's
  // own name above the very command it ran.
  it('leaves a command row without a title of its own', () => {
    expect(piToolCallIR(requestRow(PI_TOOL.Bash, { command: 'ls' })).title).toBeUndefined()
  })
})

describe('piToolCallIR edit arguments', () => {
  it('states a single substitution as the one change the title reads', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Edit, { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] }))
    expect(call.kind).toBe('edit')
    const changes = call.kind === 'edit' ? call.request.changes : []
    expect(changes).toHaveLength(1)
    expect(changes[0]).toMatchObject({ oldStr: 'before', newStr: 'after' })
  })

  it('states the size of a multi-substitution edit', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Edit, {
      path: '/project/a.ts',
      edits: [{ oldText: 'a', newText: 'b' }, { oldText: 'c', newText: 'd' }],
    }))
    expect(call.kind === 'edit' && call.request.changes).toHaveLength(2)
  })
})

describe('piToolCallIR question arguments', () => {
  const question = {
    question: 'Choose a layout',
    header: 'Layout',
    options: [
      { label: 'Compact', description: 'Small', preview: 'a worked example' },
      { label: 'Wide', description: 'Large' },
    ],
  }

  // Every one of Pi's four question tools states its question in the ARGUMENTS. An
  // empty request drew the bare wire name over an empty body, and the reader lost both
  // the question and every option it offered.
  it.each([PI_TOOL.AskUserQuestion, PI_TOOL.PlanQuestion, PI_TOOL.GoalQuestion, PI_TOOL.GoalQuestionnaire])('reads the question a %s call asked', (toolName) => {
    const call = piToolCallIR(requestRow(toolName, { questions: [question] }))
    expect(call.kind).toBe('question')
    expect(call.kind === 'question' && call.request.questions).toEqual([{
      header: 'Layout',
      question: 'Choose a layout',
      options: [
        { label: 'Compact', description: 'Small', preview: 'a worked example' },
        { label: 'Wide', description: 'Large', preview: undefined },
      ],
    }])
  })

  it('heads a single-question row with the question rather than the wire name', () => {
    expect(piToolCallIR(requestRow(PI_TOOL.GoalQuestion, { questions: [question] })).title).toBe('Choose a layout')
  })

  // Several questions cannot share one line, so the shared renderer states their
  // count -- and a title here would override it with the first question alone.
  it('states no title of its own for several questions', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.AskUserQuestion, {
      questions: [question, { question: 'Pick a colour', options: [{ label: 'Red' }] }],
    }))
    expect(call.kind === 'question' && call.request.questions).toHaveLength(2)
    expect(call.title).toBe(PI_TOOL.AskUserQuestion)
  })

  it('reads a question record the call states at the root', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.GoalQuestion, question))
    expect(call.kind === 'question' && call.request.questions.map(q => q.question)).toEqual(['Choose a layout'])
  })

  it.each([
    ['no question field', { options: [{ label: 'Red' }] }],
    ['an options list that is not an array', { question: 'Pick', options: 'Red' }],
    ['an option with no label', { question: 'Pick', options: [{ description: 'Small' }] }],
  ])('drops %s', (_name, args) => {
    const call = piToolCallIR(requestRow(PI_TOOL.AskUserQuestion, args))
    const questions = call.kind === 'question' ? call.request.questions : []
    expect(questions.flatMap(q => q.options)).toEqual([])
  })

  it('states the answer the reader chose under the question header', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.AskUserQuestion, { questions: [question] }, { content: text('Compact') }))
    expect(call.result).toEqual({ answers: [{ header: 'Layout', answer: 'Compact' }] })
  })
})

describe('piToolCallIR result slots', () => {
  it('draws a command result through the shared command body', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))
    expect(call.result).toMatchObject({ commands: [{ output: 'a.ts' }] })
  })

  it('draws a directory listing through the shared directory body', () => {
    const call = piToolCallIR(resultRow(PI_SEARCH_TOOL.List, { path: '/project' }, { content: text('a.ts\nsrc/') }))
    expect(call.result).toMatchObject({ entries: [{ path: 'a.ts' }, { path: 'src/' }] })
  })

  it('draws a grep result through the shared search body', () => {
    const call = piToolCallIR(resultRow(PI_SEARCH_TOOL.Grep, { pattern: 'answer' }, { content: text('a.ts:3: answer') }))
    expect(call.result).toMatchObject({ content: 'a.ts:3: answer', numLines: 1 })
  })

  it('draws an unrecognized extension through the shared rich-content body', () => {
    const call = piToolCallIR(resultRow('extension_lookup', { query: 'marker' }, { content: text('Extension report'), details: { count: 0 } }))
    expect(call.kind).toBe('mcp')
    expect(call.request).toMatchObject({ tool: 'extension_lookup' })
  })

  it('draws the checklist, its empty state and the note about the named task', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'get', id: 1 }, {
      content: text('#1 Inspect sample'),
      details: { action: 'get', params: { action: 'get', id: 1 }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending', description: 'Read the entry points.' }] },
    }))
    expect(call.result).toMatchObject({ items: [expect.objectContaining({ content: 'Inspect sample' })], note: 'Read the entry points.' })
    expect(call.metadata).toEqual([{ label: 'Task ID', value: '1' }])
  })

  it('draws a cleared to-do list with its own empty state', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'clear' }, {
      content: text('Cleared'),
      details: { action: 'clear', params: { action: 'clear' }, tasks: [] },
    }))
    expect(call.result).toMatchObject({ items: [] })
  })

  // These tools draw DATA, so a failed call has no body of its own and the row states
  // the error text under the shared header instead.
  it.each([PI_TOOL.Read, PI_TOOL.Edit, PI_TOOL.Write, PI_SEARCH_TOOL.Grep, PI_SEARCH_TOOL.Find, PI_SEARCH_TOOL.List])('states the error text of a failed %s', (toolName) => {
    const call = piToolCallIR(resultRow(toolName, {}, { content: text('The call refused.') }, true))
    expect(call.result).toEqual({ failure: true, text: 'The call refused.' })
  })
})

describe('piToolCall copy text', () => {
  // The plan the row DRAWS, not the envelope that holds it: the generic reading used
  // to copy `{"plan":"# ..."}` with every line break escaped.
  it('copies the plan of a completed plan row', () => {
    const plan = '# Welcome plan\n\nRead the sample.'
    const presentation = piToolCallIR(resultRow(PI_TOOL.PlanComplete, { plan }, { content: text('Plan ready for review.'), details: { plan } }))
    const meta = toolCallMeta(rowOf(presentation))
    expect(meta.copyableContent()).toBe(plan)
    expect(meta.collapsible).toBe(false)
  })

  it('copies the result text of a failed plan row, which is what that row draws', () => {
    const presentation = piToolCallIR(resultRow(PI_TOOL.PlanComplete, {}, { content: text('The plan tool refused.'), details: { plan: '# Ignored' } }, true))
    expect(toolCallMeta(rowOf(presentation)).copyableContent()).toBe('The plan tool refused.')
  })

  it('copies the raw diff an edit row draws when the diff cannot be parsed', () => {
    const diff = 'A provider diff in an unknown format'
    const presentation = piToolCallIR(resultRow(PI_TOOL.Edit, { path: '/project/a.ts' }, { content: text('Edit completed'), details: { diff } }))
    const meta = toolCallMeta(rowOf(presentation))
    expect(meta.hasDiff).toBe(false)
    expect(meta.copyableContent()).toBe(diff)
  })

  it('copies the error of a refused to-do operation', () => {
    const presentation = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'update', id: 99 }, {
      content: text('Error: #99 not found'),
      details: { action: 'update', params: { action: 'update', id: 99 }, error: '#99 not found', tasks: [] },
    }))
    expect(toolCallMeta(rowOf(presentation)).copyableContent()).toBe('#99 not found')
  })
})

describe('piToolCall', () => {
  it('reads a start event as the request of its span', () => {
    const requestCall = piToolCallIR(requestRow(PI_TOOL.Bash, { command: 'ls' }))
    expect(requestCall).toMatchObject({ id: 'call', status: 'in_progress' })
    expect(piToolRowRole(requestRow(PI_TOOL.Bash, { command: 'ls' }))).toBe('request')
  })

  it('reads a completion event as the end of its span', () => {
    expect(piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))).toMatchObject({ status: 'completed' })
    expect(piToolRowRole(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))).toBe('result')
  })

  it('reports a failed call', () => {
    expect(piToolCallIR(resultRow(PI_TOOL.Read, {}, { content: text('gone') }, true)).status).toBe('failed')
  })

  it('reports an interrupted call from the completion LeapMux recorded', () => {
    const row = piToolRow(start(PI_TOOL.Bash, { command: 'ls' }), undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(piToolCallIR(row, MessageCompletion.INTERRUPTED).status).toBe('cancelled')
  })
})

/**
 * Pi sends a one-line `description` beside every `bash` command, exactly as Claude Code
 * does, and the row header is the only place a reader ever sees it. The execute request
 * dropped it, so every Pi command row headed itself with the generic `Run command` --
 * the words the renderer falls back to when the call states nothing of its own.
 */
describe('piToolCallIR execute description', () => {
  it('carries the description the agent sent, on the request row and the result row', () => {
    const args = { command: 'ls -la', description: 'List files in current directory' }
    for (const row of [requestRow(PI_TOOL.Bash, args), resultRow(PI_TOOL.Bash, args, { content: text('ok') })]) {
      const call = piToolCallIR(row)
      expect(call.kind).toBe('execute')
      expect(call.kind === 'execute' && call.request.description).toBe('List files in current directory')
    }
  })

  it('states no description for a command that carries none', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Bash, { command: 'ls -la' }))
    expect(call.kind === 'execute' && call.request.description).toBeUndefined()
  })

  // `pickString` answers `''` for a key the arguments do not hold, and the renderer
  // treats an EMPTY description as one the agent never sent. Carrying `''` through
  // would head the row with a blank line where the command's purpose belongs.
  it('reads an empty description as no description at all', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Bash, { command: 'ls -la', description: '' }))
    expect(call.kind === 'execute' && call.request.description).toBeUndefined()
  })

  // The language and the description sit in one object literal, and PowerShell is the
  // one tool that sets both. A careless edit to either drops the other.
  it('keeps the description beside the language on a PowerShell call', () => {
    const call = piToolCallIR(requestRow(PI_POWERSHELL_TOOL, { command: 'Get-ChildItem', description: 'List the files' }))
    expect(call.kind === 'execute' && call.request).toMatchObject({ language: 'powershell', description: 'List the files' })
  })
})

/**
 * Pi words a stop and a timeout as errors, so the envelope's own status reads `failed`
 * for both. The row then headed a command the reader stopped "Error", with no exit code
 * and with the marker that explained it stripped from the body.
 */
describe('piToolCallIR execute outcome', () => {
  it.each([
    ['a stop the reader sent', 'partial output\n\nCommand aborted'],
    ['a timeout Pi reported', 'partial output\n\nCommand timed out after 30 seconds'],
  ])('words %s as cancelled rather than failed', (_name, output) => {
    const call = piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'sleep 99' }, { content: text(output) }, true))
    expect(call.status).toBe('cancelled')
    expect(call.result).toMatchObject({ commands: [{ output: 'partial output', exitCode: undefined }] })
  })

  it('leaves a non-zero exit as a failure, with its exit code', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'false' }, { content: text('boom\n\nCommand exited with code 3') }, true))
    expect(call.status).toBe('failed')
    expect(call.result).toMatchObject({ commands: [{ output: 'boom', exitCode: 3 }] })
  })

  it('leaves a command that succeeded completed', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'ls' }, { content: text('a.ts') }))
    expect(call.status).toBe('completed')
  })

  // The marker parser fires on the error path alone, so a process that PRINTS the
  // marker text on a successful run must not word the row as a stop.
  it('ignores marker-shaped output from a command that succeeded', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Bash, { command: 'echo' }, { content: text('Command aborted') }))
    expect(call.status).toBe('completed')
    expect(call.result).toMatchObject({ commands: [{ output: 'Command aborted' }] })
  })
})

/**
 * The deviation list, and the argument keys each entry on it reads.
 *
 * No type can refuse an accidental shadow here. `ToolRequestOverrides` takes a function
 * of `(args, facts)`, and a function of `args` alone is assignable to that slot -- so a
 * spread of the shared table, or an entry that merely repeats it, type-checks. This is
 * the mechanical block: the key list is pinned, and each entry is pinned against the
 * shared reading it replaces.
 */
describe('PI_TOOL_REQUEST_OVERRIDES', () => {
  it('deviates on exactly the seven kinds Pi reads from its own facts', () => {
    expect(Object.keys(PI_TOOL_REQUEST_OVERRIDES).sort()).toEqual(['agent', 'edit', 'execute', 'mcp', 'question', 'todo', 'write'])
  })

  // `command` alone, because that is the one key Pi sends. The shared entry accepts
  // `cmd` as well, and states no language at all.
  it('reads the execute command from `command`, and the shell from the tool name', () => {
    const call = piToolCallIR(requestRow(PI_POWERSHELL_TOOL, { command: 'Get-ChildItem', cmd: 'ignored', description: 'List the files' }))
    expect(call.kind === 'execute' && call.request).toEqual({ command: 'Get-ChildItem', language: 'powershell', description: 'List the files' })
    expect(piToolCallIR(requestRow(PI_TOOL.Bash, { cmd: 'ignored' })).request).toEqual({ command: '' })
    expect(DEFAULT_TOOL_REQUESTS.execute({ cmd: 'ignored' })).toEqual({ command: 'ignored' })
  })

  // The paired OPENING event states the substitutions; the row that reports them
  // carries no arguments at all. The shared entry reads a ROOT pair and not the list
  // Pi sends, so it states the file and neither side of the change.
  it('reads the edit substitutions from the paired opening event', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Edit, { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] }, { content: text('Edit completed') }))
    expect(call.kind === 'edit' && call.request.changes).toMatchObject([{ filePath: '/project/a.ts', oldStr: 'before', newStr: 'after' }])
    expect(DEFAULT_TOOL_REQUESTS.edit({ path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] }))
      .toMatchObject({ changes: [{ filePath: '/project/a.ts', oldStr: '', newStr: '' }] })
  })

  // Pi normalizes both spellings and states the LIST first, so a call that sends
  // `edits` and a root pair draws them in that order.
  it('reads the `edits` list ahead of the `oldText` and `newText` pair', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Edit, {
      path: '/project/a.ts',
      edits: [{ oldText: 'in the list', newText: 'first' }],
      oldText: 'at the root',
      newText: 'second',
    }))
    expect(call.kind === 'edit' && call.request.changes.map(change => change.oldStr)).toEqual(['in the list', 'at the root'])
  })

  // A write states the whole file, so the change it asked for is an addition. The
  // shared entry states the addition and no body: `content` is a best-effort reading
  // that every provider spells for itself, so only Pi's own entry takes it.
  it('reads the write body from `path` and `content`', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Write, { path: '/project/new.ts', content: 'export const a = 1\n' }))
    expect(call.kind === 'write' && call.request.changes).toMatchObject([{ filePath: '/project/new.ts', operation: 'add', newStr: 'export const a = 1\n' }])
    expect(DEFAULT_TOOL_REQUESTS.write({ path: '/project/new.ts', content: 'export const a = 1\n' }))
      .toMatchObject({ changes: [{ filePath: '/project/new.ts', operation: 'add', newStr: '' }] })
  })

  // The arguments answer first, and the result's own `details` answer after them.
  it('reads the launch description from the arguments ahead of the result details', () => {
    const detailed = { content: text('done'), details: { description: 'From the result', displayName: 'Explorer' } }
    const stated = piToolCallIR(resultRow(PI_TOOL.Agent, { description: 'From the arguments', prompt: 'Do it', subagent_type: 'explorer' }, detailed))
    expect(stated.kind === 'agent' && stated.request).toMatchObject({ description: 'From the arguments', agentType: 'explorer', prompt: 'Do it' })
    const recovered = piToolCallIR(resultRow(PI_TOOL.Agent, { prompt: 'Do it' }, detailed))
    expect(recovered.kind === 'agent' && recovered.request).toMatchObject({ description: 'From the result', agentType: 'Explorer' })
    // The shared entry sees the arguments alone, so the recovered launch has no words.
    expect(DEFAULT_TOOL_REQUESTS.agent({ prompt: 'Do it' })).toEqual({ description: '', prompt: 'Do it' })
  })

  // The namespace proxy states its server in its own NAME and its tool in the
  // arguments. The shared entry reads a `server` argument Pi never sends.
  it('reads the MCP server from the `mcp__` name and the tool from the `tool` argument', () => {
    const args = { tool: 'create_issue', title: 'A defect' }
    const call = piToolCallIR(requestRow('mcp__github', args))
    expect(call.kind === 'mcp' && call.request).toEqual({ server: 'github', tool: 'create_issue', args })
    expect(DEFAULT_TOOL_REQUESTS.mcp(args)).toEqual({ server: '', tool: 'create_issue', args })
  })

  // pi-mcp-adapter states the pair in the RESULT, which no argument carries.
  it('reads the MCP server and tool from the result details', () => {
    const call = piToolCallIR(resultRow('sample_lookup', { query: 'marker' }, { content: text('Found it'), details: { server: 'sample', tool: 'lookup' } }))
    expect(call.kind === 'mcp' && call.request).toEqual({ server: 'sample', tool: 'lookup', args: { query: 'marker' } })
  })

  // An extension that identifies no server states its own wire name as the tool.
  it('states the wire name as the tool of an extension with no MCP identity', () => {
    const call = piToolCallIR(requestRow('extension_lookup', { query: 'marker' }))
    expect(call.kind === 'mcp' && call.request).toEqual({ server: '', tool: 'extension_lookup', args: { query: 'marker' } })
  })

  // A tool that asks ONE question states the record at the root, and one that asks
  // several states a `questions` list. The list answers first.
  it('reads the `questions` list ahead of the question record at the root', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.AskUserQuestion, {
      question: 'At the root',
      questions: [{ question: 'In the list', header: 'Layout', options: [{ label: 'Compact', description: 'Small', preview: 'a worked example' }] }],
    }))
    expect(call.kind === 'question' && call.request.questions).toEqual([{
      header: 'Layout',
      question: 'In the list',
      options: [{ label: 'Compact', description: 'Small', preview: 'a worked example' }],
    }])
    expect(DEFAULT_TOOL_REQUESTS.question({ questions: [{ question: 'In the list' }] })).toEqual({ questions: [] })
  })

  // The checklist rides in the RESULT's `details.tasks`. An `items` argument is not
  // a key this entry reads, and the shared entry states an empty list.
  it('reads the checklist from the result details rather than from an `items` argument', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'list', items: [{ id: 9, subject: 'From the arguments' }] }, {
      content: text('1 task'),
      details: { action: 'list', params: { action: 'list' }, tasks: [{ id: 1, subject: 'From the result', status: 'pending' }] },
    }))
    expect(call.kind === 'todo' && call.request.items.map(item => item.content)).toEqual(['From the result'])
    expect(DEFAULT_TOOL_REQUESTS.todo({ items: [{ id: 9, subject: 'From the arguments' }] })).toEqual({ items: [] })
  })

  // The note is the task's own description where the call identifies one, and the
  // `description` argument otherwise.
  it('reads the to-do note from the named task ahead of the `description` argument', () => {
    const named = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'get', id: 1, description: 'From the arguments' }, {
      content: text('#1 Inspect sample'),
      details: { action: 'get', params: { action: 'get', id: 1 }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending', description: 'From the task' }] },
    }))
    expect(named.kind === 'todo' && named.request.note).toBe('From the task')
    const listed = piToolCallIR(resultRow(PI_TOOL.Todo, { action: 'list', description: 'From the arguments' }, {
      content: text('1 task'),
      details: { action: 'list', params: { action: 'list' }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending' }] },
    }))
    expect(listed.kind === 'todo' && listed.request.note).toBe('From the arguments')
  })
})

/**
 * The five kinds Pi PRODUCES and still reads from the shared table.
 *
 * They are absent from the override list on purpose: Pi spells their arguments the way
 * every other provider does. Pinning them against `DEFAULT_TOOL_REQUESTS` is what keeps
 * an override from growing back around a reading the shared table already carries --
 * `switch_mode` once spelled its own empty request, so the shared entry for the kind was
 * unreachable and nothing could see the two disagree.
 */
describe('piToolCallIR shared requests', () => {
  it.each([
    ['read', PI_TOOL.Read, { path: '/project/a.ts', offset: 5, limit: 20 }],
    ['grep', PI_SEARCH_TOOL.Grep, { pattern: 'answer', path: '/project' }],
    ['glob', PI_SEARCH_TOOL.Find, { pattern: '*.ts', path: '/project' }],
    ['list', PI_SEARCH_TOOL.List, { path: '/project' }],
    ['switch_mode', PI_TOOL.PlanComplete, { plan: '# Welcome plan' }],
  ] as const)('fills the %s request from the shared table', (kind, toolName, args) => {
    const call = piToolCallIR(requestRow(toolName, args))
    expect(call.kind).toBe(kind)
    // STRICT: a request spelled here rather than read from the table answers the same
    // fields with the absent ones simply MISSING, and `toEqual` calls that equal.
    // `switch_mode` spelled its own `{}` for exactly as long as nobody noticed.
    expect(call.request).toStrictEqual(DEFAULT_TOOL_REQUESTS[kind](args))
  })
})

/**
 * An argument record carrying every key `DEFAULT_TOOL_REQUESTS` reads, in each of its
 * spellings where it reads two.
 *
 * The delegated kinds below are compared over this ONE record, and the shape of it is
 * load-bearing. A probe that stated none of these keys would let a hand-written reader
 * and the shared entry agree on an EMPTY request, so the comparison would pass for a
 * kind that no longer delegates at all.
 */
const SHARED_ARGUMENT_PROBE: Record<string, unknown> = {
  channel: 'a shared channel',
  cmd: 'a shared cmd',
  command: 'a shared command',
  cron: '0 * * * *',
  description: 'a shared description',
  destinationPath: '/shared/dst.ts',
  destination_path: '/shared/dst2.ts',
  filePath: '/shared/a.ts',
  file_path: '/shared/a2.ts',
  id: 'an id',
  instructions: 'shared instructions',
  limit: 9,
  message: 'a shared message',
  mode: 'a shared mode',
  name: 'a shared name',
  newPath: '/shared/new.ts',
  new_path: '/shared/new2.ts',
  offset: 3,
  oldPath: '/shared/old.ts',
  old_path: '/shared/old2.ts',
  path: '/shared/a3.ts',
  paths: ['/shared/b.ts'],
  pattern: 'a shared pattern',
  prompt: 'a shared prompt',
  q: 'a shared q',
  query: 'a shared query',
  schedule: 'every hour',
  server: 'a shared server',
  skill: 'a shared skill',
  sourcePath: '/shared/src.ts',
  source_path: '/shared/src2.ts',
  spec: 'a shared spec',
  summary: 'a shared summary',
  target: 'a shared target',
  targetModeId: 'a shared target mode',
  taskId: 'task-2',
  task_id: 'task-1',
  text: 'a shared text',
  thought: 'a shared thought',
  to: 'a shared recipient',
  tool: 'a shared tool',
  triggerId: 'trigger-2',
  trigger_id: 'trigger-1',
  uri: 'https://shared-uri.example',
  url: 'https://shared.example',
}

/** The seven kinds Pi reads from its OWN facts, which `PI_TOOL_REQUEST_OVERRIDES` holds. */
const PI_OWN_REQUEST_KINDS = ['agent', 'edit', 'execute', 'mcp', 'question', 'todo', 'write'] as const

/**
 * Every other kind, which takes the shared declared request.
 *
 * `wait` is in the list and pinned by MEMBERSHIP alone: its shared entry answers the
 * constant `{ durationMs: undefined }`, so a hand-written reader that answered the same
 * constant is indistinguishable from the shared one by value. What the list still
 * states is that the kind delegates at all.
 */
const PI_SHARED_REQUEST_KINDS = [
  '',
  'agents',
  'chart',
  'delete',
  'fetch',
  'glob',
  'grep',
  'image',
  'list',
  'memory',
  'message',
  'move',
  'other',
  'read',
  'report',
  'search',
  'skill',
  'switch_mode',
  'task',
  'think',
  'trigger',
  'wait',
  'web_search',
] as const

/**
 * The reader table: one entry for each kind, checked against that kind's own request.
 *
 * Totality is the mapped type's, so a new `ToolKind` is a compile error at the table.
 * These cases pin the three statements no type makes: the keys are exactly
 * `TOOL_KINDS` at RUNTIME, each entry answers at the key that states it, and every kind
 * outside the deviation list fills the shared declared request.
 *
 * The third one is the only mechanical check that `PI_TOOL_REQUEST_OVERRIDES` has not
 * grown past its deviations. No type can do that job: an entry that reads `args` alone
 * satisfies a slot supplying `args` and the facts, so a spread of the shared table --
 * or one stray key that shadows a kind -- compiles and simply draws a different card.
 */
describe('PI_TOOL_READERS', () => {
  const probeFacts = piToolFacts(resultRow('a_tool_no_table_holds', SHARED_ARGUMENT_PROBE, { content: text('done') }), undefined)

  it('states one reader for every tool kind', () => {
    expect(Object.keys(PI_TOOL_READERS).sort()).toStrictEqual([...TOOL_KINDS].sort())
  })

  // Every reader runs against a row of a DIFFERENT kind and answers its own kind. A
  // reader that reaches for a fact this row does not carry throws here rather than in
  // the transcript, where the error boundary replaces the whole message.
  it('answers each kind at the key that states it', () => {
    for (const kind of TOOL_KINDS)
      expect(PI_TOOL_READERS[kind](probeFacts).kind, kind || 'the empty kind').toBe(kind)
  })

  it('splits every tool kind between the two lists above', () => {
    expect([...PI_OWN_REQUEST_KINDS, ...PI_SHARED_REQUEST_KINDS].sort()).toStrictEqual([...TOOL_KINDS].sort())
  })

  // The probe has to REACH the facts, or the case below compares two empty requests
  // and passes for a kind that stopped delegating.
  it('carries the whole probe into the facts the readers read', () => {
    expect(probeFacts.args).toStrictEqual(SHARED_ARGUMENT_PROBE)
  })

  it.each(PI_SHARED_REQUEST_KINDS)('fills the declared request of %s from the shared table', (kind) => {
    expect(PI_TOOL_READERS[kind](probeFacts).request).toStrictEqual(DEFAULT_TOOL_REQUESTS[kind](probeFacts.args))
  })

  // The other side of the list is pinned ENTRY BY ENTRY in the
  // `PI_TOOL_REQUEST_OVERRIDES` cases above, which state each deviation beside the
  // shared answer. A blanket "it differs from the shared entry" case cannot stand here:
  // `edit`, `write` and `question` each read a Pi record the arguments alone cannot
  // carry, so over one shared probe all three answer the shared constant.
})

/**
 * The two corrections the tool NAME cannot make, stated directly.
 *
 * The kind is decided ONCE, ahead of the readers. The payload builder used to re-enter
 * itself with the second kind and only once the call finished, so one call drew two
 * different cards.
 */
describe('piReclassify', () => {
  const factsOf = (row: PiToolRow) => piToolFacts(row, undefined)

  // A Pi extension and a Model Context Protocol bridge both answer with content
  // blocks, so both take the generic card. The uncategorized kind draws a wrench and
  // the word `Tool`, which states nothing the agent ran.
  it('folds a tool no kind table holds onto the generic card', () => {
    expect(piReclassify(factsOf(requestRow('an_extension_no_table_holds', { query: 'marker' })))).toBe('mcp')
  })

  it('demotes a to-do call whose checklist it cannot read', () => {
    expect(piReclassify(factsOf(requestRow(PI_TOOL.Todo, { action: 'teleport' })))).toBe('mcp')
  })

  it('keeps a to-do call whose checklist it read', () => {
    const row = resultRow(PI_TOOL.Todo, { action: 'list' }, {
      content: text('1 task'),
      details: { action: 'list', params: { action: 'list' }, tasks: [{ id: 1, subject: 'Inspect sample', status: 'pending' }] },
    })
    expect(piReclassify(factsOf(row))).toBe('todo')
  })

  it.each([
    [PI_TOOL.Bash, 'execute'],
    [PI_TOOL.Read, 'read'],
    [PI_TOOL.Write, 'write'],
    [PI_TOOL.Edit, 'edit'],
    [PI_SEARCH_TOOL.Grep, 'grep'],
    [PI_SEARCH_TOOL.Find, 'glob'],
    [PI_SEARCH_TOOL.List, 'list'],
  ] as const)('swaps no kind for %s', (toolName, kind) => {
    expect(piReclassify(factsOf(requestRow(toolName, {})))).toBe(kind)
  })
})

/**
 * A retained row whose TURN failed, which is the one input that tells Pi's two failure
 * facts apart.
 *
 * The worker stores the agent's own last frame and states the outcome in the completion
 * column, so a `tool_execution_start` retained under `MESSAGE_COMPLETION_ERROR` holds
 * `status === 'failed'` beside `isError === false`. Folding the two together takes the
 * error path: the request becomes `{ changes: [] }` and the reason becomes the result
 * text -- and a retained START frame has no result text, so the row loses its
 * substitutions AND states an empty reason.
 */
describe('piToolCallIR on a retained row the turn failed', () => {
  const retained = (toolName: string, args: Record<string, unknown>) =>
    piToolCallIR(piToolRow(start(toolName, args), undefined, undefined, MessageCompletion.ERROR)!, MessageCompletion.ERROR)

  it('words the row as failed while Pi flagged nothing', () => {
    const row = piToolRow(start(PI_TOOL.Edit, { path: '/project/a.ts' }), undefined, undefined, MessageCompletion.ERROR)!
    expect(row.tool.isError).toBe(false)
    expect(row.isError).toBe(false)
    expect(row.finished).toBe(true)
    expect(piToolCallIR(row, MessageCompletion.ERROR).status).toBe('failed')
  })

  it('keeps the substitutions an edit asked for, and takes no error branch', () => {
    const call = retained(PI_TOOL.Edit, { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] })
    expect(call.kind === 'edit' && call.request.changes).toMatchObject([{ filePath: '/project/a.ts', oldStr: 'before', newStr: 'after' }])
    expect(isFailedResult(call.result)).toBe(false)
  })

  it('keeps the file a write asked for, and takes no error branch', () => {
    const call = retained(PI_TOOL.Write, { path: '/project/new.ts', content: 'export const a = 1\n' })
    expect(call.kind === 'write' && call.request.changes).toMatchObject([{ filePath: '/project/new.ts', operation: 'add' }])
    expect(isFailedResult(call.result)).toBe(false)
  })

  // The other half of the pair: Pi's OWN flag does take the error branch, and states
  // the reason the call gave in words. The REQUEST stays there too --
  // `RequestedChangesBody` refuses a failed call's diff for every provider, so the
  // list draws nothing extra and only the row's title reads it.
  it('states the reason and keeps the substitutions when Pi itself flags the call', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Edit, { path: '/project/a.ts', edits: [{ oldText: 'before', newText: 'after' }] }, { content: text('No match for the old text.') }, true))
    expect(call.kind === 'edit' && call.request.changes).toMatchObject([{ filePath: '/project/a.ts', oldStr: 'before', newStr: 'after' }])
    expect(call.result).toStrictEqual({ failure: true, text: 'No match for the old text.' })
  })

  it('keeps the file a failed write asked for', () => {
    const call = piToolCallIR(resultRow(PI_TOOL.Write, { path: '/project/new.ts', content: 'export const a = 1\n' }, { content: text('The directory is read-only.') }, true))
    expect(call.kind === 'write' && call.request.changes).toMatchObject([{ filePath: '/project/new.ts', operation: 'add' }])
    expect(call.result).toStrictEqual({ failure: true, text: 'The directory is read-only.' })
  })
})

/**
 * A call that has NOT returned states no result at all.
 *
 * `ToolMessage` draws the live output the worker broadcasts only while the row is
 * `in_progress` AND its result is absent, so a result attached to a running row replaces
 * the streaming tail with an empty card. The `mcp` entry is the one that pays for it:
 * `piReclassify` routes every tool `PI_TOOL_KINDS` does not hold to that kind, so the
 * rule covers each Pi extension and each Model Context Protocol bridge.
 */
describe('piToolCallIR on a call that has not returned', () => {
  it('states no result for an unrecognized extension that is still running', () => {
    const call = piToolCallIR(requestRow('extension_lookup', { query: 'marker' }))
    expect(call.kind).toBe('mcp')
    expect(call.status).toBe('in_progress')
    expect(call.result).toBeUndefined()
    expect(call.request).toMatchObject({ tool: 'extension_lookup' })
  })

  // The second route to the same entry: a to-do operation whose checklist this build
  // cannot read draws the shared card, and it must wait for its answer just the same.
  it('states no result for a running to-do call whose checklist it cannot read', () => {
    const call = piToolCallIR(requestRow(PI_TOOL.Todo, { action: 'teleport' }))
    expect(call.kind).toBe('mcp')
    expect(call.result).toBeUndefined()
  })

  // Every kind, from the one table, so a later entry that fills a result early fails
  // here rather than on the row a reader watches.
  it('states no result on the opening frame of any kind', () => {
    for (const toolName of [PI_TOOL.Bash, PI_TOOL.Read, PI_TOOL.Write, PI_TOOL.Edit, PI_TOOL.Agent, PI_SEARCH_TOOL.Grep, PI_SEARCH_TOOL.Find, PI_SEARCH_TOOL.List, 'an_extension_no_table_holds'])
      expect(piToolCallIR(minimalRequestRow(toolName)).result, toolName).toBeUndefined()
  })
})
