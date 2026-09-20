import type { SearchResult } from '../../../model/searchResult'
import type { ToolKind } from '../../../model/toolKind'
import type { CopilotToolFacts, CopilotToolRow } from './toolCall'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { copilotFrame, copilotToolComplete, copilotToolStart } from '~/test-support/copilotFixtures'
import { todoTitleOf } from '~/test-support/toolCallFixture'
import { toolCallRow } from '../../../model/row'
import { isToolFailureResult, typedResult } from '../../../model/toolCall'
import { deriveToolCallStatus } from '../../../model/toolCallLifecycle'
import { TOOL_KINDS } from '../../../model/toolKind'
import { imagesForRow } from '../../../results/rowImages'
import { DEFAULT_TOOL_REQUESTS, toolRequestFor } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { copilotToolKind } from '../toolKinds'
import { COPILOT_TOOL_READERS, COPILOT_TOOL_REQUEST_OVERRIDES, copilotReclassify, copilotToolCall, copilotToolFacts, copilotToolRow } from './toolCall'

const CALL = 'copilot-call'

function parsed(row: Record<string, unknown>): ParsedMessageContent {
  return { ...input(row), supplementalContent: undefined } as ParsedMessageContent
}

/** The finished row for one call, resolved from its start frame and its completion. */
function resultRow(
  toolName: string,
  args: Record<string, unknown>,
  outcome: { success?: boolean, result?: Record<string, unknown>, error?: Record<string, unknown> },
): CopilotToolRow {
  const row = copilotToolRow(copilotToolComplete(CALL, outcome), toolName, parsed(copilotToolStart(CALL, toolName, args)))
  expect(row).not.toBeNull()
  return row!
}

/** The facts of one RUNNING call: its start frame alone, with no completion beside it. */
function startFacts(toolName: string, args: Record<string, unknown>): CopilotToolFacts {
  return copilotToolFacts(copilotToolRow(copilotToolStart(CALL, toolName, args))!)
}

/** The facts of one finished call. */
function resultFacts(
  toolName: string,
  args: Record<string, unknown>,
  outcome: { success?: boolean, result?: Record<string, unknown>, error?: Record<string, unknown> } = { success: true, result: { content: 'ok' } },
): CopilotToolFacts {
  return copilotToolFacts(resultRow(toolName, args, outcome))
}

describe('copilotToolRow', () => {
  it('reads a failed call from its error, which states why it stopped', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      result: { content: 'partial output' },
      error: { code: 'ENOENT', message: 'no such file' },
    })
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('failed')
    expect(row.raw).toEqual({ code: 'ENOENT', message: 'no such file' })
  })

  it('keeps the output a cancelled call produced before the turn stopped', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      result: { content: 'partial output' },
      error: { code: 'interrupted', message: 'aborted' },
    })
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('cancelled')
    expect(row.raw).toEqual({ content: 'partial output' })
  })

  it('falls back to the error when a cancelled call carries no result', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      error: { code: 'interrupted', message: 'aborted' },
    })
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('cancelled')
    expect(row.raw).toEqual({ code: 'interrupted', message: 'aborted' })
  })
})

describe('copilotToolCall background shells', () => {
  // The four calls act on a shell the session already started: each one identifies it by
  // id and carries no command, which is a task rather than an execution.
  it.each([
    [COPILOT_TOOL.ReadBash, 'output'],
    [COPILOT_TOOL.StopBash, 'stop'],
    [COPILOT_TOOL.ListBash, 'list'],
    [COPILOT_TOOL.WriteBash, 'input'],
  ])('reads %s as a task on the shell it names', (toolName, action) => {
    const call = copilotToolCall(resultRow(toolName, { shellId: '7' }, { result: { content: 'shell output' } }))
    expect(call.kind).toBe('task')
    expect(call.kind === 'task' ? call.request : null).toEqual({ action, taskId: '7' })
    expect(call.kind === 'task' && call.result && 'output' in call.result ? call.result.output : undefined).toBe('shell output')
  })

  // `ListBash` identifies no shell, because it asks about every one of them.
  it('states no shell id for the call that lists them all', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ListBash, {}, { result: { content: 'one shell' } }))
    expect(call.kind === 'task' ? call.request : null).toEqual({ action: 'list', taskId: undefined })
  })

  it('states the failed outcome of a shell call that stopped', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ReadBash, { shellId: '7' }, {
      success: false,
      error: { code: 'ENOENT', message: 'no such shell' },
    }))
    expect(call.status).toBe('failed')
    expect(call.kind === 'task' && call.result && 'outcome' in call.result ? call.result.outcome : undefined).toBe('failed')
  })
})

describe('copilotToolCall shell results', () => {
  it('states the shell metadata when the exit block reports no numeric code', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: {
        content: 'command output',
        contents: [{ type: 'shell_exit', shellId: '7', cwd: '/project', outputFilePath: '/project/.tmp/out.log' }],
      },
    })
    const call = copilotToolCall(row)
    expect(call.metadata).toEqual([
      { label: 'Shell ID', value: '7' },
      { label: 'Directory', value: '/project' },
      { label: 'Output file', value: '/project/.tmp/out.log' },
    ])
    // The block is the row's own metadata, so it never reaches the extra content as
    // raw JSON.
    expect(call.extraContent).toBeUndefined()
    expect(call.kind === 'execute' && call.result && 'commands' in call.result ? call.result.commands[0]?.exitCode : undefined).toBeUndefined()
  })

  it('reports the exit code the block states', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: { content: 'command output', contents: [{ type: 'shell_exit', shellId: '0', exitCode: 3 }] },
    })
    const call = copilotToolCall(row)
    expect(call.kind === 'execute' && call.result && 'commands' in call.result ? call.result.commands[0]?.exitCode : undefined).toBe(3)
    expect(call.metadata).toEqual([{ label: 'Shell ID', value: '0' }])
  })

  it('drops a content block that repeats a text field the row already shows', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: {
        content: 'model summary',
        detailedContent: 'full output',
        contents: [
          { type: 'text', text: 'model summary' },
          { type: 'text', text: 'full output' },
          { type: 'text', text: 'a note neither field carries' },
        ],
      },
    })
    const call = copilotToolCall(row)
    expect(call.extraContent).toEqual([{ type: 'text', text: 'a note neither field carries' }])
  })

  it('drops a content block that repeats a text field including its shell trailer', () => {
    const trailer = '\n<shellId: 0 completed with exit code 0>'
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: {
        content: `model summary${trailer}`,
        detailedContent: 'full output',
        contents: [{ type: 'text', text: 'model summary' }],
      },
    })
    expect(copilotToolCall(row).extraContent).toBeUndefined()
  })

  // The trailer states the code in decimal text of unbounded length, so it takes the
  // same safe-integer gate as the structured block. Without it the row headed itself
  // `Error (exit 100000000000000000000)`, a number no platform can report.
  it('refuses a trailer exit code no platform can report', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: { content: 'output\n<shellId: 0 completed with exit code 99999999999999999999>' },
    })
    const call = copilotToolCall(row)
    expect(call.kind === 'execute' && call.result && 'commands' in call.result ? call.result.commands[0]?.exitCode : undefined).toBeUndefined()
  })

  it('still reads a trailer code that a platform can report', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: { content: 'output\n<shellId: 0 completed with exit code 7>' },
    })
    const call = copilotToolCall(row)
    expect(call.kind === 'execute' && call.result && 'commands' in call.result ? call.result.commands[0]?.exitCode : undefined).toBe(7)
  })
})

describe('copilotToolCall search results', () => {
  function grepResult(context: Record<string, unknown>): SearchResult {
    const row = resultRow(COPILOT_TOOL.Grep, { pattern: 'hit', ...context }, {
      result: { content: 'a.ts:1:hit\nb.ts:2:hit' },
    })
    const call = copilotToolCall(row)
    // The grep kind stays; the match count states the total.
    expect(call.kind).toBe('grep')
    return (call.kind === 'grep' && call.result ? call.result : null) as SearchResult
  }

  it.each([
    ['absent', {}],
    ['zero', { C: 0 }],
    ['the string zero', { C: '0' }],
    ['empty', { C: '' }],
    ['false', { C: false }],
    ['null', { C: null }],
    ['a word', { C: 'all' }],
  ])('counts the matched lines when the context argument is %s', (_label, context) => {
    expect(grepResult(context)?.matchCount).toBe(2)
  })

  it.each([
    ['a number', { C: 2 }],
    ['a numeric string', { after_context: '3' }],
  ])('drops the match count when the context argument states one as %s', (_label, context) => {
    expect(grepResult(context)?.matchCount).toBeUndefined()
  })

  it('keeps the drive prefix while it counts unnumbered Windows paths', () => {
    const row = resultRow(COPILOT_TOOL.Grep, { pattern: 'hit' }, {
      result: { content: 'C:\\repo\\a.ts:hit one\nC:\\repo\\b.ts:hit two' },
    })
    const call = copilotToolCall(row)
    expect(call.kind === 'grep' ? typedResult(call)?.numFiles : undefined).toBe(2)
  })
})

describe('copilotToolCall to-do lists', () => {
  // No `title` of its own: `todoRenderer` composes the same words from the request,
  // and a copy here was a second place for the wording to drift.
  it('states a cleared list rather than a count of zero', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.UpdateTodo, { todos: '' }))!
    const call = copilotToolCall(row)
    expect(call.title).toBeUndefined()
    expect(call.kind === 'todo' && call.request.items).toEqual([])
    expect(todoTitleOf(call)).toBe('To-do list')
  })

  it('states the count for a list that holds items', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.UpdateTodo, { todos: '- [ ] first\n- [x] second' }))!
    const call = copilotToolCall(row)
    expect(call.title).toBeUndefined()
    expect(todoTitleOf(call)).toBe('2 tasks')
  })
})

describe('copilotToolCall rich content', () => {
  it('states the arguments in an unrecognized tool\'s own body', () => {
    const row = resultRow('custom_tool', { query: 'needle' }, {
      result: { contents: [{ type: 'text', text: 'a block' }] },
    })
    const call = copilotToolCall(row)
    expect(call.kind).toBe('mcp')
    expect(JSON.stringify(call.request)).toContain('needle')
  })

  it('states no arguments beside a recognized tool, whose header already carries them', () => {
    const row = resultRow(COPILOT_TOOL.View, { path: '/project/a.txt' }, {
      result: { content: 'file text', contents: [{ type: 'text', text: 'a block' }] },
    })
    expect(copilotToolCall(row).extraContent).toEqual([{ type: 'text', text: 'a block' }])
  })
})

describe('copilotToolCall subagent launches', () => {
  function launch(args: Record<string, unknown>) {
    return copilotToolCall(copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.Task, args))!)
  }

  it('titles the row from the description', () => {
    const call = launch({ description: 'Inspect project structure', agent_type: 'explore', prompt: 'Read the entry points.' })
    expect(call.kind).toBe('agent')
    expect(call.title).toBe('Inspect project structure')
    expect(call.request).toEqual({ description: 'Inspect project structure', agentType: 'explore', prompt: 'Read the entry points.' })
  })

  // A launch states the instruction in `description` and the subagent's own label in
  // `name`, and it can carry either one.
  it('titles the row from the subagent label when the launch describes nothing', () => {
    const call = launch({ name: 'Reviewer', prompt: 'Read the entry points.' })
    expect(call.title).toBe('Reviewer')
    expect(call.kind === 'agent' && call.request.description).toBe('Reviewer')
  })

  it('falls back to the shared word when the launch carries neither', () => {
    expect(launch({ prompt: 'Read the entry points.' }).title).toBe('Task')
  })

  it('states one description on the request card and on the report', () => {
    const row = resultRow(COPILOT_TOOL.Task, { name: 'Reviewer' }, { success: true, result: { content: 'Found two' } })
    const call = copilotToolCall(row)
    expect(call.kind === 'agent' && call.request.description).toBe('Reviewer')
    expect(call.kind === 'agent' && call.result && 'agents' in call.result ? call.result.agents[0] : null).toEqual(expect.objectContaining({ description: 'Reviewer', body: 'Found two' }))
  })
})

/**
 * The twenty-five tool names that moved off the uncategorized kind.
 *
 * That kind's renderer PRINTED THE RAW ARGUMENTS, so every one of these rows said
 * what it was about until the move gave it a typed request that read the wrong keys
 * -- or read none at all.
 */
describe('copilot typed requests', () => {
  function callOf(toolName: string, args: Record<string, unknown>, outcome: { success?: boolean, result?: Record<string, unknown>, error?: Record<string, unknown> } = { success: true, result: { content: 'ok' } }) {
    return copilotToolCall(resultRow(toolName, args, outcome))
  }

  it('names the file a delete removed', () => {
    const call = callOf(COPILOT_TOOL.Delete, { path: '/p/gone.ts' })
    expect(call.kind).toBe('delete')
    expect(call.kind === 'delete' && call.request.changes[0]?.filePath).toBe('/p/gone.ts')
    expect(call.kind === 'delete' && call.result && 'changes' in call.result ? call.result.changes[0]?.filePath : undefined).toBe('/p/gone.ts')
  })

  it.each([
    [COPILOT_TOOL.Sql, { query: 'SELECT 1' }, 'SELECT 1', 'sql'],
    [COPILOT_TOOL.SessionStoreSql, { query: 'SELECT 2' }, 'SELECT 2', 'sql'],
    [COPILOT_TOOL.WritePowerShell, { shellId: '7', input: 'Get-Date' }, 'Get-Date', 'powershell'],
    [COPILOT_TOOL.Bash, { command: 'ls' }, 'ls', undefined],
  ])('states the command %s runs', (toolName, args, command, language) => {
    const call = callOf(toolName, args)
    expect(call.kind).toBe('execute')
    expect(call.kind === 'execute' && call.request.command).toBe(command)
    expect(call.kind === 'execute' && call.request.language).toBe(language)
  })

  it('states the question ask_user asked and the choices it offered', () => {
    const call = callOf(COPILOT_TOOL.AskUser, { question: 'Which one?', choices: ['The first', 'The second'] }, { success: true, result: { content: 'The first' } })
    expect(call.kind).toBe('question')
    expect(call.kind === 'question' && call.request.questions[0]?.question).toBe('Which one?')
    expect(call.kind === 'question' && call.request.questions[0]?.options.map(option => option.label)).toEqual(['The first', 'The second'])
    // The ANSWER's header is the question, never the tool name.
    expect(call.kind === 'question' && call.result && 'answers' in call.result ? call.result.answers[0]?.header : undefined).toBe('Which one?')
  })

  it.each([
    [COPILOT_TOOL.Skill, { skill: 'deploy' }, 'deploy'],
    [COPILOT_TOOL.ExtensionsManage, {}, COPILOT_TOOL.ExtensionsManage],
  ])('names the skill %s ran', (toolName, args, name) => {
    const call = callOf(toolName, args)
    expect(call.kind).toBe('skill')
    expect(call.kind === 'skill' && call.request.name).toBe(name)
  })

  it.each(['path', 'file_path', 'filePath'])('reads the file a view states under %s', (key) => {
    const call = callOf(COPILOT_TOOL.View, { [key]: '/p/a.ts' }, { success: true, result: { content: 'const a = 1' } })
    expect(call.kind).toBe('read')
    expect(call.kind === 'read' && call.request.path).toBe('/p/a.ts')
  })
})

/**
 * A background shell answers in the same shape `bash` does.
 *
 * The `read_bash` family moved from `execute` to `task` and reproduced none of it: the
 * runtime's trailer printed literally, the exit code was never read, and the shell id,
 * the directory and the output file all vanished.
 */
describe('copilot background shells', () => {
  const contents = [{ type: 'shell_exit', exitCode: 1, shellId: '7', cwd: '/p', outputFilePath: '/p/out.log' }]

  it('states the exit code, the trailer-free output and the shell rows', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ReadBash, { shellId: '7' }, {
      success: true,
      result: { content: 'partial output\n<shellId: 7 completed with exit code 1>', contents },
    }))
    expect(call.kind).toBe('task')
    const source = call.kind === 'task' && call.result && 'outcome' in call.result ? call.result : undefined
    expect(source?.output).toBe('partial output')
    expect(source?.outcome).toBe('failed')
    expect(call.metadata).toEqual([
      { label: 'Shell ID', value: '7' },
      { label: 'Directory', value: '/p' },
      { label: 'Output file', value: '/p/out.log' },
    ])
  })

  it('reports a shell that exited cleanly as completed', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ReadBash, { shellId: '7' }, {
      success: true,
      result: { content: 'done', contents: [{ type: 'shell_exit', exitCode: 0, shellId: '7' }] },
    }))
    expect(call.kind === 'task' && call.result && 'outcome' in call.result ? call.result.outcome : undefined).toBe('completed')
  })
})

describe('copilot rich content', () => {
  const contents = [{ type: 'image', data: 'aGk=', mimeType: 'image/png' }]

  it.each([
    [COPILOT_TOOL.Task, { description: 'Probe' }],
    [COPILOT_TOOL.UpdateTodo, { todos: '- [ ] one' }],
    [COPILOT_TOOL.ReadBash, { shellId: '7' }],
    [COPILOT_TOOL.Edit, { path: '/p/a.ts', old_str: 'x', new_str: 'y' }],
    [COPILOT_TOOL.Delete, { path: '/p/a.ts' }],
    [COPILOT_TOOL.AskUser, { question: 'Which?' }],
    [COPILOT_TOOL.Move, { path: '/p/b.ts', source: '/p/a.ts' }],
  ])('carries the blocks %s attached', (toolName, args) => {
    const call = copilotToolCall(resultRow(toolName, args, { success: true, result: { content: 'ok', contents } }))
    expect(call.extraContent?.some(item => item.type === 'image')).toBe(true)
  })

  // The blocks ride every state of the call, not the success one alone.
  it('carries the blocks a failed skill attached', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.Skill, { skill: 'deploy' }, { success: false, error: { message: 'no such skill', contents } }))
    expect(call.extraContent?.some(item => item.type === 'image')).toBe(true)
  })

  // Copilot carries EVERY picture in `extraContent` rather than in the call's own image
  // list, so a failure path that drops the blocks drops each image with them -- and the
  // image tab, which addresses a picture by its index in `imagesForRow`, loses it too.
  it.each([
    [COPILOT_TOOL.Grep, { pattern: 'needle' }],
    [COPILOT_TOOL.Glob, { pattern: '*.ts' }],
    [COPILOT_TOOL.SearchCodeSubagent, { query: 'needle' }],
    [COPILOT_TOOL.View, { path: '/p/a.ts' }],
    [COPILOT_TOOL.WebFetch, { url: 'https://example.com' }],
  ])('carries the blocks a failed %s attached', (toolName, args) => {
    const call = copilotToolCall(resultRow(toolName, args, { success: false, error: { message: 'it broke', contents } }))
    expect(call.extraContent?.some(item => item.type === 'image')).toBe(true)
  })

  // The same list through the derivation an image tab addresses, so the count a reader
  // scrolls is the one that changed.
  it.each([
    [COPILOT_TOOL.Grep, { pattern: 'needle' }],
    [COPILOT_TOOL.View, { path: '/p/a.ts' }],
    [COPILOT_TOOL.WebFetch, { url: 'https://example.com' }],
  ])('lists the picture a failed %s attached in the row it draws', (toolName, args) => {
    const row = toolCallRow(copilotToolCall(resultRow(toolName, args, { success: false, error: { message: 'it broke', contents } })), 'result', { request: false, result: false })
    expect(imagesForRow(row)).toHaveLength(1)
  })

  // `JSON.parse` re-reads every number as a double, so a round trip through it drew a
  // different id than the runtime sent.
  it('keeps a structured id past the double range exactly', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.Task, { description: 'Probe' }, {
      success: true,
      result: { content: 'ok', structuredContent: '{"id":9007199254740993}' },
    }))
    const raw = call.extraContent?.find(item => item.type === 'unknown')
    expect(raw?.type === 'unknown' && String(raw.raw)).toContain('9007199254740993')
  })

  // `GenericToolBody` draws both fields with no guard, so the same words in each drew
  // the failure twice under one card.
  it('states a failed generic call once', () => {
    const call = copilotToolCall(resultRow('some_unknown_tool', {}, { success: false, error: { message: 'it broke' } }))
    expect(call.kind).toBe('mcp')
    const source = call.kind === 'mcp' && call.result && 'content' in call.result ? call.result : undefined
    expect(source?.content).toEqual([])
    expect(source?.error).toBe('it broke')
  })
})

/**
 * The turn's own outcome reaches a Copilot row.
 *
 * `copilotToolStatus` tested the completion for `interrupted` alone, so the `failed`
 * outcome the worker records never reached one.
 */
describe('copilot retained rows', () => {
  it('reports a failed turn on the completion row', () => {
    const row = copilotToolRow(
      copilotToolComplete(CALL, { success: true, result: { content: 'ok' } }),
      COPILOT_TOOL.Bash,
      parsed(copilotToolStart(CALL, COPILOT_TOOL.Bash, { command: 'ls' })),
      MessageCompletion.ERROR,
    )
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('failed')
  })

  it('reports a failed turn on a retained start row', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.Bash, { command: 'ls' }), COPILOT_TOOL.Bash, undefined, MessageCompletion.ERROR)
    expect(row?.finished).toBe(true)
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('failed')
  })

  // A start row carries NO result, so `completed` over it claims an answer that never
  // arrived: the turn ending well says nothing about a call whose completion the
  // runtime never sent.
  it('refuses to call a retained start row completed', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.Bash, { command: 'ls' }), COPILOT_TOOL.Bash, undefined, MessageCompletion.COMPLETE)
    expect(row?.finished).toBe(true)
    // A succeeded outcome completes nothing and a start lands no result, so the
    // row states no status word at all -- never a completed one.
    expect(row && deriveToolCallStatus(row.lifecycle, row.lifecycle.resultFrameLanded)).toBe('incomplete')
  })
})

/**
 * The edit-family request carries the keys the model DECLARES, and no other one.
 *
 * `FileChangeRequest` states `changes` and an optional `replaceAll`. This branch put
 * an extra `patchText` on the object, and nothing caught it: TypeScript checks a fresh
 * object literal for excess properties only in a contextually typed position, and the
 * request was a `const` that the returned literal then referenced as a VARIABLE. The
 * model carried a key no renderer reads.
 *
 * The assertion reads the KEYS rather than comparing objects. `toEqual` ignores a
 * property whose value is `undefined`, so it passes straight over an undeclared key
 * that holds one and proves nothing.
 */
describe('copilotToolCall edit-family requests', () => {
  const UNREADABLE_PATCH = 'this text is not a patch envelope'
  const PATCH = '*** Begin Patch\n*** Update File: /project/a.ts\n@@\n-before\n+after\n*** End Patch'

  it('states only the declared keys for the patch it read', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ApplyPatch, { input: PATCH }, {
      success: true,
      result: { content: 'applied' },
    }))
    expect(call.kind).toBe('edit')
    expect(Object.keys(call.request).sort()).toEqual(['changes'])
  })

  // A patch this build cannot read names NO file, and a file operation states the file
  // it acts on (invariant I7). `copilotReclassify` degrades the row at the frame, where
  // the arguments the tool was called with are still in hand: the shared degrade states
  // the empty change list in their place, and the card then reads as a call nobody made.
  it('draws a patch it could not read as an uncategorized call', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ApplyPatch, { input: UNREADABLE_PATCH }, {
      success: true,
      result: { content: 'applied' },
    }))
    expect(call.kind).toBe('mcp')
    expect(call.kind === 'mcp' && call.request.args).toMatchObject({ input: UNREADABLE_PATCH })
  })

  // The row still identifies the tool that ran. The uncategorized card heads itself
  // with the tool its request names, which is where the name goes once the kind is gone.
  it('names the tool of a patch it could not read', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ApplyPatch, { input: UNREADABLE_PATCH }, { success: true, result: { content: '' } }))
    expect(call.kind === 'mcp' && call.request.tool).toBe(COPILOT_TOOL.ApplyPatch)
  })

  // The REQUEST stays on a failure. `RequestedChangesBody` refuses a failed call's diff
  // for every provider, so the list draws nothing extra -- but the row's TITLE is
  // composed from it, and an empty one heads the row with the bare kind word.
  it('keeps the file a failed edit asked to change', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.Edit, { path: '/project/a.ts', old_str: 'before', new_str: 'after' }, {
      success: false,
      error: { message: 'no match for the old text' },
    }))
    expect(call.kind).toBe('edit')
    expect(call.kind === 'edit' && call.request.changes).toMatchObject([{ filePath: '/project/a.ts', oldStr: 'before', newStr: 'after' }])
    expect(call.result).toStrictEqual({ failure: true, text: 'no match for the old text' })
  })

  it('keeps the file a failed create asked to write', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.Create, { path: '/project/new.ts', file_text: 'export const a = 1\n' }, {
      success: false,
      error: { message: 'the directory is read-only' },
    }))
    expect(call.kind).toBe('write')
    expect(call.kind === 'write' && call.request.changes).toMatchObject([{ filePath: '/project/new.ts', operation: 'add' }])
  })

  it('keeps both paths a failed move asked for', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.Move, { source: '/project/old.ts', path: '/project/new.ts' }, {
      success: false,
      error: { message: 'the destination exists' },
    }))
    expect(call.kind).toBe('move')
    expect(call.kind === 'move' && call.request.changes).toMatchObject([{ previousPath: '/project/old.ts', filePath: '/project/new.ts' }])
  })
})

/**
 * Whether Copilot RECOGNIZED an empty search result.
 *
 * The extractor already reads Copilot's own empty wording to blank `fallbackContent`
 * and to empty the line list. The flag states the same fact for the row, so the
 * renderer no longer measures a provider's bytes against LeapMux's own summary prose
 * to guess it.
 */
describe('copilotToolCall empty search results', () => {
  const searchCall = (toolName: string, output: string, args: Record<string, unknown> = {}) =>
    copilotToolCall(resultRow(toolName, args, { success: true, result: { content: output } }))

  const emptyOf = (call: ReturnType<typeof searchCall>) =>
    (call.result as SearchResult | undefined)?.empty

  it('reads each wording Copilot prints when it matched nothing', () => {
    expect(emptyOf(searchCall(COPILOT_TOOL.Glob, 'No files found', { pattern: '*.ts' }))).toBe(true)
    expect(emptyOf(searchCall(COPILOT_TOOL.Grep, 'No matches found', { pattern: 'x' }))).toBe(true)
    expect(emptyOf(searchCall(COPILOT_TOOL.Grep, '', { pattern: 'x' }))).toBe(true)
  })

  it('states no empty result for a search that found something', () => {
    expect(emptyOf(searchCall(COPILOT_TOOL.Glob, 'src/a.ts', { pattern: '*.ts' }))).toBe(false)
    expect(emptyOf(searchCall(COPILOT_TOOL.Grep, 'src/a.ts:1:hit', { pattern: 'x' }))).toBe(false)
  })
})

/**
 * The facts one row states, collected once.
 *
 * Two argument records, and the difference decides where a picture opens. `rawArgs` is
 * what the runtime sent; `args` carries the three derivations the requests read.
 */
describe('copilotToolFacts', () => {
  it('keeps the arguments the runtime sent apart from the derived copy', () => {
    const facts = resultFacts(COPILOT_TOOL.View, { path: '/p/a.ts', view_range: [5, 6] }, { success: true, result: { content: 'a\nb' } })
    expect(facts.rawArgs).toEqual({ path: '/p/a.ts', view_range: [5, 6] })
    expect(facts.args).toEqual({ path: '/p/a.ts', view_range: [5, 6], offset: 5, limit: 2 })
  })

  // The patch states the file, and the derived copy carries it so the edit request can
  // read it. The untouched record must NOT, because a picture block with no `uri` of
  // its own takes that path -- and the call itself never sent one.
  it('folds the file one patch operation names into the derived copy alone', () => {
    const facts = startFacts(COPILOT_TOOL.ApplyPatch, { input: '*** Begin Patch\n*** Add File: made.txt\n+first\n*** End Patch' })
    expect(facts.rawArgs.path).toBeUndefined()
    expect(facts.args.path).toBe('made.txt')
    expect(facts.args.content).toBe('first\n')
  })

  it('recovers the single search target under the neutral key', () => {
    expect(startFacts(COPILOT_TOOL.Glob, { pattern: '*.ts', paths: ['/src'] }).args.path).toBe('/src')
    expect(startFacts(COPILOT_TOOL.Glob, { pattern: '*.ts', paths: ['/src', '/lib'] }).args.path).toBeUndefined()
  })

  // The one state that has matches to state. Its absence is what the search reader
  // reads as "this call has not answered", so a running or a failed row must leave it
  // undefined whatever the output says.
  it('states the matches of a finished search alone', () => {
    expect(resultFacts(COPILOT_TOOL.Grep, { pattern: 'hit' }, { success: true, result: { content: 'a.ts:1:hit' } }).search?.empty).toBe(false)
    expect(startFacts(COPILOT_TOOL.Grep, { pattern: 'hit' }).search).toBeUndefined()
    expect(resultFacts(COPILOT_TOOL.Grep, { pattern: 'hit' }, { success: false, error: { message: 'a.ts' } }).search).toBeUndefined()
    expect(resultFacts(COPILOT_TOOL.Bash, { command: 'ls' }).search).toBeUndefined()
  })

  it('heads a row by its description, then its tool name, then the shared word', () => {
    expect(startFacts(COPILOT_TOOL.Bash, { command: 'ls', description: 'List it' }).title).toBe('List it')
    expect(startFacts(COPILOT_TOOL.Bash, { command: 'ls' }).title).toBe(COPILOT_TOOL.Bash)
    expect(copilotToolFacts(copilotToolRow(copilotToolStart(CALL, '', {}))!).title).toBe('Tool')
  })
})

/**
 * The three swaps that move a row off the kind its tool name states.
 *
 * `copilotToolKind` reads the name alone. Each swap here reads a fact the name cannot
 * carry: the patch this build parsed, the shape of a view's answer, and the mode a
 * search reported in.
 */
describe('copilotReclassify', () => {
  it('reads a one-operation patch as the operation it states', () => {
    const patch = (body: string) => copilotReclassify(startFacts(COPILOT_TOOL.ApplyPatch, { input: `*** Begin Patch\n${body}\n*** End Patch` }))
    expect(patch('*** Add File: made.txt\n+first')).toBe('write')
    expect(patch('*** Delete File: gone.ts')).toBe('delete')
    expect(patch('*** Update File: a.ts\n@@\n-before\n+after')).toBe('edit')
  })

  // Several operations state no single verb, so the row keeps the edit kind and lists
  // the files instead.
  it('keeps the edit kind for a patch with several operations', () => {
    expect(copilotReclassify(startFacts(COPILOT_TOOL.ApplyPatch, {
      input: '*** Begin Patch\n*** Add File: made.txt\n+first\n*** Delete File: gone.ts\n*** End Patch',
    }))).toBe('edit')
  })

  it('reads a view that answered prose and named no file as a report', () => {
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.View, {}, { success: true, result: { content: 'a note' } }))).toBe('report')
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.View, { path: '/p/a.ts' }, { success: true, result: { message: 'a note' } }))).toBe('report')
  })

  it('keeps the read kind for a view that answered a file body', () => {
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.View, { path: '/p/a.ts' }, { success: true, result: { content: 'const a = 1' } }))).toBe('read')
  })

  it('keeps the read kind for a view that answered nothing, and for one that failed', () => {
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.View, {}, { success: true, result: {} }))).toBe('read')
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.View, {}, { success: false, error: { message: 'no such file' } }))).toBe('read')
    expect(copilotReclassify(startFacts(COPILOT_TOOL.View, { path: '/p/a.ts' }))).toBe('read')
  })

  it('reads a grep that listed files as a glob', () => {
    const files = { success: true, result: { content: 'a.ts\nb.ts' } }
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.Grep, { pattern: 'hit', output_mode: 'files_with_matches' }, files))).toBe('glob')
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.Grep, { pattern: 'hit', output_mode: 'content' }, files))).toBe('grep')
  })

  // A search that listed no file keeps its own kind: an LSP or a tool search draws
  // through the search renderer, and the grep wording would state matches it never
  // counted.
  it('keeps the search kind for a tool search that listed no file', () => {
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.Lsp, {}, { success: true, result: { content: 'a.ts:1:hit' } }))).toBe('search')
  })

  it('leaves every other row at the kind its tool name states', () => {
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.Bash, { command: 'ls' }))).toBe('execute')
    expect(copilotReclassify(resultFacts(COPILOT_TOOL.Delete, { path: '/p/a.ts' }))).toBe('delete')
    expect(copilotReclassify(resultFacts('a_tool_no_table_holds', {}))).toBe('mcp')
  })
})

/**
 * The kinds Copilot reads differently from the shared request table.
 *
 * Each case pins the argument KEYS the entry reads and the ORDER it reads them in, and
 * states the shared answer beside it where the two differ. No type can do this job: an
 * entry that takes `args` alone satisfies a slot that supplies `args` and the facts, so
 * a stray key that shadows a kind -- or a spread of the whole shared table -- compiles
 * and simply draws a different card.
 */
describe('COPILOT_TOOL_REQUEST_OVERRIDES', () => {
  const requestOf = <K extends ToolKind>(kind: K, facts: CopilotToolFacts) =>
    toolRequestFor(kind, facts.args, facts, COPILOT_TOOL_REQUEST_OVERRIDES)

  it('deviates on exactly the twenty kinds its own vocabulary and its facts answer', () => {
    expect(Object.keys(COPILOT_TOOL_REQUEST_OVERRIDES).sort()).toEqual([
      'agent',
      'agents',
      'delete',
      'edit',
      'execute',
      'fetch',
      'glob',
      'grep',
      'mcp',
      'memory',
      'message',
      'move',
      'question',
      'report',
      'search',
      'skill',
      'switch_mode',
      'task',
      'todo',
      'write',
    ])
  })

  // `read` and `web_search` are absent on purpose: Copilot spells both exactly as the
  // shared table does, so each has one reading.
  it('shadows neither kind that the shared table already answers for Copilot', () => {
    const read = resultFacts(COPILOT_TOOL.View, { file_path: '/p/a.ts', offset: 3, limit: 2 })
    expect(requestOf('read', read)).toEqual(DEFAULT_TOOL_REQUESTS.read(read.args))
    expect(requestOf('read', read)).toEqual({ path: '/p/a.ts', offset: 3, limit: 2 })
    const searched = resultFacts(COPILOT_TOOL.WebSearch, { q: 'needle' })
    expect(requestOf('web_search', searched)).toEqual(DEFAULT_TOOL_REQUESTS.web_search(searched.args))
    expect(requestOf('web_search', searched)).toEqual({ query: 'needle' })
  })

  it('reads the shell a task acts on from shellId before shell_id, and its verb from the tool name', () => {
    expect(requestOf('task', startFacts(COPILOT_TOOL.ReadBash, { shellId: '7', shell_id: '9', task_id: '3' })))
      .toEqual({ action: 'output', taskId: '7' })
    expect(requestOf('task', startFacts(COPILOT_TOOL.StopBash, { shell_id: '9' }))).toEqual({ action: 'stop', taskId: '9' })
    expect(requestOf('task', startFacts(COPILOT_TOOL.ListBash, {}))).toEqual({ action: 'list', taskId: undefined })
    expect(requestOf('task', startFacts(COPILOT_TOOL.WriteBash, { shellId: '7' }))).toEqual({ action: 'input', taskId: '7' })
    expect(requestOf('task', startFacts(COPILOT_TOOL.ReadAgent, { agent: 'a1' }))).toEqual({ action: 'output', taskId: undefined })
    // The shared entry reads neither shell spelling, and states one verb for every tool.
    expect(DEFAULT_TOOL_REQUESTS.task({ shellId: '7' })).toEqual({ action: 'other', taskId: undefined })
  })

  it('reads a command from command, then query, then script, then input', () => {
    expect(requestOf('execute', startFacts(COPILOT_TOOL.Bash, { command: 'ls', query: 'SELECT 1', script: 's', input: 'i', description: 'List it' })))
      .toEqual({ command: 'ls', language: undefined, description: 'List it' })
    expect(requestOf('execute', startFacts(COPILOT_TOOL.Sql, { query: 'SELECT 1', script: 's', input: 'i' })))
      .toEqual({ command: 'SELECT 1', language: 'sql', description: undefined })
    expect(requestOf('execute', startFacts(COPILOT_TOOL.SessionStoreSql, { query: 'SELECT 2' })))
      .toEqual({ command: 'SELECT 2', language: 'sql', description: undefined })
    expect(requestOf('execute', startFacts(COPILOT_TOOL.WritePowerShell, { script: 'Get-Date', input: 'i' })))
      .toEqual({ command: 'Get-Date', language: 'powershell', description: undefined })
    expect(requestOf('execute', startFacts(COPILOT_TOOL.LocalShell, { input: 'i' })))
      .toEqual({ command: 'i', language: undefined, description: undefined })
    // The shared entry reads `command` and `cmd`, and neither of Copilot's other three.
    expect(DEFAULT_TOOL_REQUESTS.execute({ query: 'SELECT 1' })).toEqual({ command: '', description: undefined })
  })

  it('reads an edit from the patch first, then from the replacement the tool carries', () => {
    const patched = startFacts(COPILOT_TOOL.ApplyPatch, { input: '*** Begin Patch\n*** Update File: a.ts\n@@\n-before\n+after\n*** End Patch' })
    expect(requestOf('edit', patched).changes.map(change => [change.filePath, change.operation])).toEqual([['a.ts', 'edit']])
    const replaced = startFacts(COPILOT_TOOL.Edit, { path: '/p/a.ts', old_str: 'x', new_str: 'y' })
    expect(requestOf('edit', replaced).changes).toEqual([
      { filePath: '/p/a.ts', operation: 'edit', oldStr: 'x', newStr: 'y', structuredPatch: null, showLineNumbers: false },
    ])
    expect(requestOf('write', startFacts(COPILOT_TOOL.Create, { path: '/p/new.ts', file_text: 'body' })).changes).toEqual([
      { filePath: '/p/new.ts', operation: 'add', oldStr: '', newStr: 'body', structuredPatch: null, showLineNumbers: false },
    ])
    // A `str_replace_editor` view states no replacement, so it asks for no change.
    expect(requestOf('edit', startFacts(COPILOT_TOOL.StrReplaceEditor, { command: 'view', path: '/p/a.ts' })).changes).toEqual([])
  })

  it('reads a removal from every file-path spelling, and states the operation', () => {
    for (const key of ['filePath', 'path', 'file_path']) {
      expect(requestOf('delete', startFacts(COPILOT_TOOL.Delete, { [key]: '/p/gone.ts' })).changes)
        .toEqual([{ filePath: '/p/gone.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null }])
    }
    expect(requestOf('delete', startFacts(COPILOT_TOOL.Delete, {})).changes).toEqual([])
  })

  it('reads the MCP pair from the start event, and falls back to the tool name', () => {
    const named = copilotToolFacts(copilotToolRow(copilotFrame(COPILOT_EVENT.ToolStarted, {
      toolCallId: CALL,
      toolName: 'github-mcp-server-web_search',
      mcpServerName: 'github-mcp-server',
      mcpToolName: 'web_search',
      arguments: { q: 'needle' },
    }))!)
    expect(requestOf('mcp', named)).toEqual({ server: 'github-mcp-server', tool: 'web_search', args: { q: 'needle' } })
    const plain = startFacts('an_extension_tool', { q: 'needle' })
    expect(requestOf('mcp', plain)).toEqual({ server: '', tool: 'an_extension_tool', args: { q: 'needle' } })
    // The shared entry reads both halves out of the ARGUMENTS, which Copilot never
    // states there: the dash-namespaced name cannot be split.
    expect(DEFAULT_TOOL_REQUESTS.mcp({ q: 'needle' })).toEqual({ args: { q: 'needle' }, server: '', tool: '' })
  })

  it('names a skill from skill before name, then from the tool that ran it', () => {
    expect(requestOf('skill', startFacts(COPILOT_TOOL.Skill, { skill: 'deploy', name: 'the tool' }))).toEqual({ name: 'deploy' })
    expect(requestOf('skill', startFacts(COPILOT_TOOL.Skill, { name: 'deploy' }))).toEqual({ name: 'deploy' })
    expect(requestOf('skill', startFacts(COPILOT_TOOL.ExtensionsManage, {}))).toEqual({ name: COPILOT_TOOL.ExtensionsManage })
    // The shared entry reads the two keys in the OTHER order, and states the arguments.
    expect(DEFAULT_TOOL_REQUESTS.skill({ skill: 'deploy', name: 'the tool' }).name).toBe('the tool')
  })

  it('reads a sent message from message before content, then from the result text', () => {
    expect(requestOf('message', resultFacts(COPILOT_TOOL.WriteAgent, { agent: 'a1', message: 'Ready', content: 'Stale' })))
      .toEqual({ to: 'a1', text: 'Ready' })
    expect(requestOf('message', resultFacts(COPILOT_TOOL.WriteAgent, { agent: 'a1', content: 'Ready' })))
      .toEqual({ to: 'a1', text: 'Ready' })
    expect(requestOf('message', resultFacts(COPILOT_TOOL.WriteAgent, { agent: 'a1' }, { success: true, result: { content: 'the sent text' } })))
      .toEqual({ to: 'a1', text: 'the sent text' })
    // The shared entry reads neither `agent` nor `content`, and never the result text.
    expect(DEFAULT_TOOL_REQUESTS.message({ agent: 'a1', content: 'Ready' })).toEqual({ to: undefined, text: '', summary: undefined })
  })

  it('describes a launch by its description before the subagent label', () => {
    expect(requestOf('agent', startFacts(COPILOT_TOOL.Task, { description: 'Probe it', name: 'Reviewer', agent_type: 'explore', prompt: 'Read it' })))
      .toEqual({ description: 'Probe it', agentType: 'explore', prompt: 'Read it' })
    expect(requestOf('agent', startFacts(COPILOT_TOOL.Task, { name: 'Reviewer', prompt: 'Read it' })))
      .toEqual({ description: 'Reviewer', agentType: '', prompt: 'Read it' })
    // The shared entry reads neither `name` nor `agent_type`, and takes `instructions`
    // as a second spelling of the prompt, which Copilot never sends.
    expect(DEFAULT_TOOL_REQUESTS.agent({ name: 'Reviewer', prompt: 'Read it' })).toEqual({ description: '', prompt: 'Read it' })
  })

  it('reads a to-do list from the one markdown checklist argument', () => {
    expect(requestOf('todo', startFacts(COPILOT_TOOL.UpdateTodo, { todos: '- [x] first\n- [ ] second' })).items.map(item => [item.content, item.status]))
      .toEqual([['first', 'completed'], ['second', 'pending']])
    expect(requestOf('todo', startFacts(COPILOT_TOOL.UpdateTodo, { todos: '' })).items).toEqual([])
    // The shared entry states an empty list, so the row said nothing it had planned.
    expect(DEFAULT_TOOL_REQUESTS.todo({ todos: '- [ ] first' })).toEqual({ items: [] })
  })

  it('reads a question and the choices it offered', () => {
    expect(requestOf('question', startFacts(COPILOT_TOOL.AskUser, { question: 'Which one?', choices: ['first', 'second', 7] })))
      .toEqual({ questions: [{ question: 'Which one?', options: [{ label: 'first' }, { label: 'second' }] }] })
    expect(requestOf('question', startFacts(COPILOT_TOOL.AskUser, { choices: ['first'] }))).toEqual({ questions: [] })
    // The shared entry states an empty list, so the row said nothing it had asked.
    expect(DEFAULT_TOOL_REQUESTS.question({ question: 'Which one?' })).toEqual({ questions: [] })
  })

  it('reads a move from path for the destination and source for the origin', () => {
    expect(requestOf('move', startFacts(COPILOT_TOOL.Move, { path: '/p/b.ts', source: '/p/a.ts' })).changes)
      .toEqual([{ filePath: '/p/b.ts', previousPath: '/p/a.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null }])
    // The shared move list holds neither spelling, so the origin went missing.
    expect(DEFAULT_TOOL_REQUESTS.move({ path: '/p/b.ts', source: '/p/a.ts' }).changes[0]?.previousPath).toBeUndefined()
  })

  it.each(['glob', 'grep', 'search'] as const)('reads a %s pattern from pattern before query, and one target from path', (kind) => {
    expect(requestOf(kind, startFacts(COPILOT_TOOL.Glob, { pattern: '*.ts', query: '*.md', paths: ['/src'] })))
      .toEqual({ pattern: '*.ts', paths: ['/src'] })
    expect(requestOf(kind, startFacts(COPILOT_TOOL.Grep, { query: 'needle', paths: ['/src', '/lib'] })))
      .toEqual({ pattern: 'needle', paths: ['/src', '/lib'] })
    expect(requestOf(kind, startFacts(COPILOT_TOOL.Glob, {}))).toEqual({ pattern: '', paths: [] })
  })

  it('reads a fetch from url alone', () => {
    expect(requestOf('fetch', startFacts(COPILOT_TOOL.WebFetch, { url: 'https://example.com' }))).toEqual({ url: 'https://example.com' })
    expect(requestOf('fetch', startFacts(COPILOT_TOOL.WebFetch, { uri: 'https://example.com' }))).toEqual({ url: '' })
    // The shared entry reads `uri` as well, which Copilot's `web_fetch` never sends.
    expect(DEFAULT_TOOL_REQUESTS.fetch({ uri: 'https://example.com' })).toEqual({ url: 'https://example.com' })
  })

  it('reads a mode switch from mode alone, and states no worktree target', () => {
    expect(requestOf('switch_mode', startFacts(COPILOT_TOOL.ExitPlanMode, { mode: 'agent', targetModeId: 'plan', target: 'a-branch' })))
      .toEqual({ mode: 'agent' })
    expect(requestOf('switch_mode', startFacts(COPILOT_TOOL.ExitPlanMode, { targetModeId: 'plan' }))).toEqual({ mode: undefined })
    // The shared entry reads both further keys, and neither reaches a Copilot row.
    expect(DEFAULT_TOOL_REQUESTS.switch_mode({ targetModeId: 'plan', target: 'a-branch' })).toEqual({ mode: 'plan', target: 'a-branch' })
  })

  it.each(['memory', 'report'] as const)('states the whole argument record as a %s payload, empty or not', (kind) => {
    expect(requestOf(kind, startFacts(COPILOT_TOOL.ContextBoard, { note: 'remember it' }))).toEqual({ payload: { note: 'remember it' } })
    expect(requestOf(kind, startFacts(COPILOT_TOOL.ContextBoard, {}))).toEqual({ payload: {} })
    // The shared entry drops an empty record, and a board READ states no field at all.
    expect(DEFAULT_TOOL_REQUESTS[kind]({})).toEqual({ payload: undefined })
  })

  it('reads an agent list from query alone', () => {
    expect(requestOf('agents', startFacts(COPILOT_TOOL.ListAgents, { query: 'reviewer', q: 'other', channel: 'a-room' })))
      .toEqual({ query: 'reviewer' })
    expect(requestOf('agents', startFacts(COPILOT_TOOL.ListAgents, { q: 'other' }))).toEqual({ query: undefined })
    // The shared entry reads `q` and a channel, and neither reaches a Copilot row.
    expect(DEFAULT_TOOL_REQUESTS.agents({ q: 'other', channel: 'a-room' })).toEqual({ query: 'other', channel: 'a-room' })
  })
})

/**
 * The reader table: one entry for each kind, checked against that kind's own request.
 *
 * Totality is the mapped type's, so a new `ToolKind` is a compile error here. These
 * cases pin what the type cannot: that each entry answers its OWN key, and which kinds
 * no Copilot tool takes.
 */
describe('COPILOT_TOOL_READERS', () => {
  const payloadOf = <K extends ToolKind>(kind: K, facts: CopilotToolFacts) => COPILOT_TOOL_READERS[kind](facts)

  it('answers its own kind at every key', () => {
    const facts = resultFacts(COPILOT_TOOL.Bash, { command: 'ls' })
    for (const kind of TOOL_KINDS)
      expect(payloadOf(kind, facts).kind, kind).toBe(kind)
  })

  // The tool table plus the Model Context Protocol fallback IS the inventory: every
  // swap lands on a kind some tool name already states.
  it('leaves eight kinds that no Copilot tool takes', () => {
    const named = new Set<ToolKind>([...Object.values(COPILOT_TOOL).map(copilotToolKind), 'mcp'])
    expect(TOOL_KINDS.filter(kind => !named.has(kind)))
      .toEqual(['unspecified', 'chart', 'image', 'list', 'other', 'think', 'trigger', 'wait'])
    expect((['write', 'delete', 'report', 'glob'] as const).filter(kind => !named.has(kind))).toEqual([])
  })

  // A kind no tool takes still states its DECLARED fields, from the shared table. The
  // renderers read those with no guard, so a `{ args }` there reaches
  // `call.request.changes[0]` and throws the whole message into the error boundary.
  it('fills the declared request of a kind no tool takes, from the shared table', () => {
    const facts = startFacts('a_tool_no_table_holds', { path: '/p', prompt: 'draw it' })
    expect(payloadOf('list', facts).request).toEqual({ path: '/p' })
    expect(payloadOf('image', facts).request).toEqual({ prompt: 'draw it' })
    expect(payloadOf('think', facts).request).toEqual({ text: '' })
    expect(payloadOf('wait', facts).request).toEqual({ durationMs: undefined })
  })
})

/**
 * The two rows the report kind draws, which state their headers differently.
 *
 * `task_complete` states its own tool word. A `view` that `copilotReclassify` moved
 * here states none: the word `view` over a page of prose says nothing its body does not.
 */
describe('copilotToolCall reports', () => {
  it('heads a native report with its tool name and a reclassified view with nothing', () => {
    const native = copilotToolCall(resultRow(COPILOT_TOOL.TaskComplete, {}, { success: true, result: { content: 'all done' } }))
    expect(native.kind).toBe('report')
    expect(native.title).toBe(COPILOT_TOOL.TaskComplete)
    const viewed = copilotToolCall(resultRow(COPILOT_TOOL.View, { query: 'needle' }, { success: true, result: { message: 'a note' } }))
    expect(viewed.kind).toBe('report')
    expect(viewed.title).toBeUndefined()
    expect(viewed.kind === 'report' ? viewed.request.payload : undefined).toEqual({ query: 'needle' })
    expect(viewed.result).toStrictEqual({ text: 'a note', format: 'markdown' })
  })
})

/**
 * The FORMAT each prose kind answers with, which picks the body `ProseResultBody` draws.
 *
 * `markdown` draws the formatted body; `plain` draws a `<pre>` block that prints every
 * asterisk, dash and table pipe as a literal character. The two kinds whose answer is a
 * written page take markdown, and the three that answer one composed line take plain --
 * the same split Claude and the Agent Client Protocol family already draw.
 */
describe('copilotProseAnswer formats', () => {
  const proseOf = (toolName: string, args: Record<string, unknown>, output: string) =>
    copilotToolCall(resultRow(toolName, args, { success: true, result: { content: output } })).result

  it.each([
    [COPILOT_TOOL.ListAgents, {}, '| Agent | Model |\n| --- | --- |\n| **explore** | fast |'],
    [COPILOT_TOOL.TaskComplete, {}, '## Result\n\n- Read **two** files.'],
    [COPILOT_TOOL.ReportProgress, {}, '- Read **two** files.'],
  ])('answers markdown for %s', (toolName, args, output) => {
    expect(proseOf(toolName, args, output)).toStrictEqual({ text: output, format: 'markdown' })
  })

  it.each([
    [COPILOT_TOOL.Skill, { skill: 'deploy' }, 'Loaded the deploy skill.'],
    [COPILOT_TOOL.ExitPlanMode, {}, 'Switched to the build mode.'],
    [COPILOT_TOOL.ContextBoard, {}, 'note: keep the old bytes'],
  ])('answers plain for %s', (toolName, args, output) => {
    expect(proseOf(toolName, args, output)).toStrictEqual({ text: output, format: 'plain' })
  })

  // A failed prose call states its reason instead, in every one of the five kinds.
  it('states the reason rather than a prose body for a failed roster', () => {
    const call = copilotToolCall(resultRow(COPILOT_TOOL.ListAgents, {}, { success: false, error: { message: 'no agents configured' } }))
    expect(call.result).toStrictEqual({ failure: true, text: 'no agents configured' })
  })
})

/**
 * A call the reader STOPPED keeps the body it already collected.
 *
 * `CopilotToolFacts.failed` states the runtime's OWN fault flag and nothing else. A
 * cancelled row is not a fault: `copilotToolRow` reads its `result` object rather than
 * its `error`, so the lines, the matches and the substitutions it produced are all
 * there, and every reader below then draws them. Testing the row STATUS instead threw
 * that away and restated the same words as the reason the call gave.
 *
 * The outcome word is unaffected in each case. `toolCallStatusOutcome` composes the
 * `Interrupted` header from the row's own status, never from the result.
 */
describe('a cancelled Copilot call', () => {
  /** One finished call that the turn stopped: Copilot reports it with this error code. */
  const cancelledCall = (toolName: string, args: Record<string, unknown>, result: Record<string, unknown>) =>
    copilotToolCall(resultRow(toolName, args, { success: false, result, error: { code: 'interrupted', message: 'aborted' } }))

  it('keeps the lines a stopped read already returned', () => {
    const call = cancelledCall(COPILOT_TOOL.View, { path: '/p/a.ts' }, { content: 'const a = 1\nconst b = 2' })
    expect(call.status).toBe('cancelled')
    expect(isToolFailureResult(call.result)).toBe(false)
    expect(call.kind === 'read' ? typedResult(call)?.fallbackContent : undefined).toBe('const a = 1\nconst b = 2')
  })

  it('keeps the matches a stopped search already listed', () => {
    const call = cancelledCall(COPILOT_TOOL.Grep, { pattern: 'needle', output_mode: 'files_with_matches' }, { content: 'a.ts\nb.ts' })
    expect(call.status).toBe('cancelled')
    expect(isToolFailureResult(call.result)).toBe(false)
    expect(call.kind === 'glob' ? typedResult(call)?.filenames : undefined).toStrictEqual(['a.ts', 'b.ts'])
  })

  it('keeps the substitution a stopped edit asked for', () => {
    const call = cancelledCall(COPILOT_TOOL.Edit, { path: '/p/a.ts', old_str: 'before', new_str: 'after' }, { content: 'partial' })
    expect(call.status).toBe('cancelled')
    expect(isToolFailureResult(call.result)).toBe(false)
    expect(call.kind === 'edit' ? typedResult(call)?.changes.map(change => change.newStr) : undefined).toStrictEqual(['after'])
  })

  it('keeps the words a stopped skill printed, as prose rather than as a reason', () => {
    const call = cancelledCall(COPILOT_TOOL.Skill, { skill: 'deploy' }, { content: 'Loaded the deploy skill.' })
    expect(call.status).toBe('cancelled')
    expect(call.result).toStrictEqual({ text: 'Loaded the deploy skill.', format: 'plain' })
  })

  // The uncategorized card draws `content` and `error` with no guard of its own, so
  // the two never hold the same words. A stopped call's output is content it produced.
  it('draws the output of a stopped server call as content rather than as an error', () => {
    const call = cancelledCall('a_tool_no_table_holds', { q: 'needle' }, { content: 'two hits so far' })
    expect(call.kind).toBe('mcp')
    // No error and no structured copy: both keys are absent rather than undefined.
    expect(typedResult(call)).toStrictEqual({
      content: [{ type: 'text', text: 'two hits so far' }],
    })
  })

  // The task body states the SHELL's own state, and `statesOwnOutcome` suppresses the
  // shared `Interrupted` header for it -- so `completed` here would be the only word a
  // read the reader stopped ever draws.
  it('words a stopped shell read as stopped rather than as completed or failed', () => {
    const call = cancelledCall(COPILOT_TOOL.ReadBash, { shellId: '7' }, { content: 'still running' })
    expect(call.kind).toBe('task')
    expect(call.kind === 'task' ? typedResult(call)?.outcome : undefined).toBe('stopped')
  })

  // The exit code is the shell's own answer and outranks the interruption: a shell
  // that reported one finished, whatever happened to the call that read it.
  it('keeps the exit code a stopped shell read reported', () => {
    const call = cancelledCall(COPILOT_TOOL.ReadBash, { shellId: '7' }, { content: 'boom\n<shellId: 7 completed with exit code 1>' })
    expect(call.kind === 'task' ? typedResult(call)?.outcome : undefined).toBe('failed')
  })

  // The FAILED half of the same ladder, unchanged: the runtime flagged a fault, so the
  // reason it gave is the body.
  it.each([
    [COPILOT_TOOL.View, { path: '/p/a.ts' }],
    [COPILOT_TOOL.Grep, { pattern: 'needle' }],
    [COPILOT_TOOL.Edit, { path: '/p/a.ts', old_str: 'before', new_str: 'after' }],
    [COPILOT_TOOL.Skill, { skill: 'deploy' }],
  ])('still states the reason a failed %s gave', (toolName, args) => {
    const call = copilotToolCall(resultRow(toolName, args, { success: false, error: { message: 'permission denied' } }))
    expect(call.status).toBe('failed')
    expect(call.result).toStrictEqual({ failure: true, text: 'permission denied' })
  })
})
