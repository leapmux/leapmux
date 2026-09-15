import type { CopilotToolRow } from './toolPresentation'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { COPILOT_TOOL } from '~/generated/contracts/copilot-protocol'
import { copilotToolComplete, copilotToolStart } from '~/test-support/copilotFixtures'
import { input } from '../testUtils'
import { copilotToolPresentation, copilotToolRow } from './toolPresentation'

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

describe('copilotToolRow', () => {
  it('reads a failed call from its error, which states why it stopped', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      result: { content: 'partial output' },
      error: { code: 'ENOENT', message: 'no such file' },
    })
    expect(row.status).toBe('failed')
    expect(row.raw).toEqual({ code: 'ENOENT', message: 'no such file' })
  })

  it('keeps the output a cancelled call produced before the turn stopped', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      result: { content: 'partial output' },
      error: { code: 'interrupted', message: 'aborted' },
    })
    expect(row.status).toBe('cancelled')
    expect(row.raw).toEqual({ content: 'partial output' })
  })

  it('falls back to the error when a cancelled call carries no result', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      success: false,
      error: { code: 'interrupted', message: 'aborted' },
    })
    expect(row.status).toBe('cancelled')
    expect(row.raw).toEqual({ code: 'interrupted', message: 'aborted' })
  })
})

describe('copilotToolPresentation shell results', () => {
  it('states the shell metadata when the exit block reports no numeric code', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: {
        content: 'command output',
        contents: [{ type: 'shell_exit', shellId: '7', cwd: '/project', outputFilePath: '/project/.tmp/out.log' }],
      },
    })
    const model = copilotToolPresentation(row)
    expect(model.metadata).toEqual([
      { label: 'Shell ID', value: '7' },
      { label: 'Directory', value: '/project' },
      { label: 'Output file', value: '/project/.tmp/out.log' },
    ])
    // The block is the row's own metadata, so it never reaches the extra content as
    // raw JSON.
    expect(model.additionalContent).toBeUndefined()
    expect(model.body.type === 'command' && model.body.source.exitCode).toBeUndefined()
  })

  it('reports the exit code the block states', () => {
    const row = resultRow(COPILOT_TOOL.Bash, { command: 'run' }, {
      result: { content: 'command output', contents: [{ type: 'shell_exit', shellId: '0', exitCode: 3 }] },
    })
    const model = copilotToolPresentation(row)
    expect(model.body.type === 'command' && model.body.source.exitCode).toBe(3)
    expect(model.metadata).toEqual([{ label: 'Shell ID', value: '0' }])
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
    const model = copilotToolPresentation(row)
    expect(model.additionalContent?.content).toEqual([{ type: 'text', text: 'a note neither field carries' }])
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
    expect(copilotToolPresentation(row).additionalContent).toBeUndefined()
  })
})

describe('copilotToolPresentation search results', () => {
  function grepModel(context: Record<string, unknown>) {
    const row = resultRow(COPILOT_TOOL.Grep, { pattern: 'hit', ...context }, {
      result: { content: 'a.ts:1:hit\nb.ts:2:hit' },
    })
    const body = copilotToolPresentation(row).body
    expect(body.type).toBe('search')
    return body.type === 'search' ? body.source : null
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
    expect(grepModel(context)?.matches).toBe(2)
  })

  it.each([
    ['a number', { C: 2 }],
    ['a numeric string', { after_context: '3' }],
  ])('drops the match count when the context argument states one as %s', (_label, context) => {
    expect(grepModel(context)?.matches).toBeUndefined()
  })
})

describe('copilotToolPresentation to-do lists', () => {
  it('states a cleared list rather than a count of zero', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.UpdateTodo, { todos: '' }))!
    const model = copilotToolPresentation(row)
    expect(model.title).toBe('To-do list cleared')
    expect(model.body).toEqual({ type: 'todo', items: [] })
  })

  it('states the count for a list that holds items', () => {
    const row = copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.UpdateTodo, { todos: '- [ ] first\n- [x] second' }))!
    expect(copilotToolPresentation(row).title).toBe('2 tasks')
  })
})

describe('copilotToolPresentation rich content', () => {
  it('states the arguments in an unrecognized tool\'s own body', () => {
    const row = resultRow('custom_tool', { query: 'needle' }, {
      result: { contents: [{ type: 'text', text: 'a block' }] },
    })
    const body = copilotToolPresentation(row).body
    expect(body.type).toBe('mcp')
    expect(body.type === 'mcp' && body.source.argsJson).toContain('needle')
  })

  it('states no arguments beside a recognized tool, whose header already carries them', () => {
    const row = resultRow(COPILOT_TOOL.View, { path: '/project/a.txt' }, {
      result: { content: 'file text', contents: [{ type: 'text', text: 'a block' }] },
    })
    expect(copilotToolPresentation(row).additionalContent?.argsJson).toBe('')
  })
})

describe('copilotToolPresentation subagent launches', () => {
  function launch(args: Record<string, unknown>) {
    return copilotToolPresentation(copilotToolRow(copilotToolStart(CALL, COPILOT_TOOL.Task, args))!)
  }

  it('titles the row from the description', () => {
    const model = launch({ description: 'Inspect project structure', agent_type: 'explore', prompt: 'Read the entry points.' })
    expect(model.kind).toBe('agent')
    expect(model.title).toBe('Inspect project structure')
    expect(model.agentRequest).toEqual({ toolName: COPILOT_TOOL.Task, description: 'Inspect project structure', agentType: 'explore', prompt: 'Read the entry points.' })
    expect(model.body).toEqual({ type: 'text' })
  })

  // A launch states the instruction in `description` and the subagent's own label in
  // `name`, and it can carry either one.
  it('titles the row from the subagent label when the launch describes nothing', () => {
    const model = launch({ name: 'Reviewer', prompt: 'Read the entry points.' })
    expect(model.title).toBe('Reviewer')
    expect(model.agentRequest?.description).toBe('Reviewer')
  })

  it('falls back to the shared word when the launch carries neither', () => {
    expect(launch({ prompt: 'Read the entry points.' }).title).toBe('Task')
  })

  it('states one description on the request card and on the report', () => {
    const row = resultRow(COPILOT_TOOL.Task, { name: 'Reviewer' }, { success: true, result: { content: 'Found two' } })
    const model = copilotToolPresentation(row)
    expect(model.agentRequest?.description).toBe('Reviewer')
    expect(model.body).toEqual({ type: 'agent', source: expect.objectContaining({ description: 'Reviewer', body: 'Found two' }) })
  })
})
