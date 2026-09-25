import { describe, expect, it } from 'vitest'
import { failedResult, proseResult } from '../../../model/toolCall'
import { toolCallDisplayName } from '../../../results/tools/header'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { grokToolCallAdapter, grokToolName } from './toolCall'

/** Grok's identity of one call, as `_meta["x.ai/tool"]` states it. */
function identity(name: string, extra: Record<string, unknown> = {}) {
  return { _meta: { 'x.ai/tool': { version: 1, name, ...extra } } }
}

/** The call one pending frame draws: the name as the title, the arguments, and no kind. */
function pending(name: string, rawInput: Record<string, unknown>, fields: Record<string, unknown> = {}) {
  return acpToolCall({ sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', title: name, rawInput, ...identity(name), ...fields }, grokToolCallAdapter, undefined)
}

/** The call one finished frame draws, with its text and any other field Grok states. */
function ended(name: string, rawInput: Record<string, unknown>, text: string, fields: Record<string, unknown> = {}) {
  return acpToolCall({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'call',
    status: 'completed',
    title: name,
    rawInput,
    content: [{ type: 'content', content: { type: 'text', text } }],
    ...identity(name),
    ...fields,
  }, grokToolCallAdapter, undefined)
}

describe('grokToolName', () => {
  it('reads the identity ahead of the title', () => {
    expect(grokToolName({ title: 'List `/w`', ...identity('list_dir') })).toBe('list_dir')
  })

  it('takes a title that is a known or a branch tool when the identity states no name', () => {
    for (const name of ['grep', 'spawn_subagent', 'todo_write', 'use_tool', 'workflow'])
      expect(grokToolName({ title: name }), name).toBe(name)
    expect(grokToolName({ title: 'grep', _meta: { 'x.ai/tool': { name: '' } } })).toBe('grep')
  })

  // The title comes off the wire, and a prototype member must not read as a tool.
  it('answers no name for a prose title or a title that spells a prototype member', () => {
    for (const title of ['Read `/p/a.ts`', 'toString', '__proto__', 'constructor', ''])
      expect(grokToolName({ title }), title).toBe('')
    expect(grokToolName({})).toBe('')
  })
})

describe('grokToolCallAdapter', () => {
  describe('the arguments', () => {
    // The presentation update states the command only in the canonical input.
    it('fills a key that the call omits from the canonical input', () => {
      const call = pending('run_terminal_command', { description: 'List' }, identity('run_terminal_command', { input: { command: 'ls -la' } }))
      expect(call.kind === 'execute' && call.request.command).toBe('ls -la')
    })

    it('keeps the call\'s own key over the canonical one', () => {
      const call = pending('run_terminal_command', { command: 'ls' }, identity('run_terminal_command', { input: { command: 'ls -la' } }))
      expect(call.kind === 'execute' && call.request.command).toBe('ls')
    })

    it('keeps a path the call states over its target directory', () => {
      const call = pending('list_dir', { target_directory: '/a', path: '/b' })
      expect(call.kind === 'list' && call.request.path).toBe('/b')
    })

    it('never draws the variant tag as an argument', () => {
      const call = pending('docs__search', { variant: 'Mcp', q: 'x' }, identity('docs__search', { namespace: 'mcp' }))
      expect(call.kind === 'mcp' && call.request.args).toEqual({ q: 'x' })
    })
  })

  describe('a tool that Grok does not list', () => {
    it('keeps its name on the build of its wire kind', () => {
      const call = pending('a_new_fetcher', { url: 'https://example.com' }, { kind: 'fetch' })
      expect(call.kind).toBe('fetch')
      expect(call.name).toBe('a_new_fetcher')
    })

    // Grok's opening frame states no kind. The row then takes the uncategorized card,
    // and its header must state the tool that ran, not the word "Tool".
    it('heads the opening frame of a tool from a later release with its name', () => {
      const call = pending('a_later_tool', { q: 1 })
      expect(call.kind).toBe('mcp')
      expect(call.label).toBeUndefined()
      expect(call.kind === 'mcp' && call.request).toMatchObject({ server: '', tool: 'a_later_tool', args: { q: 1 } })
      expect(toolCallDisplayName(call)).toBe('a_later_tool')
    })

    it('reads an MCP tool that states the namespace and no server as the tool alone', () => {
      const call = pending('lookup', { q: 'x' }, identity('lookup', { namespace: 'mcp' }))
      expect(call.kind === 'mcp' && call.request).toMatchObject({ server: '', tool: 'lookup', args: { q: 'x' } })
    })
  })

  describe('use_tool', () => {
    it('reads a wrapped tool with no server as the tool alone', () => {
      const call = pending('use_tool', { tool_name: 'lookup', tool_input: { q: 'x' } })
      expect(call.kind === 'mcp' && call.request).toMatchObject({ server: '', tool: 'lookup', args: { q: 'x' } })
    })

    // With no wrapped name, the row states the call as Grok sent it and invents no server.
    it('reads a call that wraps no tool as the shared card of the call itself', () => {
      const frame = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', title: 'use_tool', rawInput: { tool_input: { q: 'x' } }, ...identity('use_tool') }
      const call = acpToolCall(frame, grokToolCallAdapter, undefined)
      expect(call.kind).toBe('mcp')
      expect(call.name).toBe('use_tool')
      expect(call.request).toEqual(acpToolCall(frame, undefined, undefined).request)
      expect(call.kind === 'mcp' && call.request).toMatchObject({ server: '', args: { tool_input: { q: 'x' } } })
    })

    it('states no result before the server answers', () => {
      expect(pending('use_tool', { tool_name: 'linear__list_issues', tool_input: {} }).result).toBeUndefined()
    })

    it('states the reason of a failed call as its body', () => {
      const call = ended('use_tool', { tool_name: 'linear__list_issues', tool_input: {} }, 'Server offline', { status: 'failed' })
      expect(call.result).toEqual(failedResult('Server offline'))
    })
  })

  describe('a task call', () => {
    it('states the one id of a kill, and a stop when it finished', () => {
      const call = ended('kill_command_or_subagent', { task_id: 'task-1' }, 'Task task-1 was killed.')
      expect(call.kind === 'task' && call.request).toEqual({ action: 'stop', taskId: 'task-1' })
      expect(call.result).toEqual({ outcome: 'stopped', output: 'Task task-1 was killed.' })
    })

    it('drops each id that is empty or no string, and reads task_id when none is left', () => {
      expect(pending('get_command_or_subagent_output', { task_ids: ['', 7, null], task_id: 'solo' }).request).toEqual({ action: 'output', taskId: 'solo' })
      expect(pending('get_command_or_subagent_output', { task_ids: [] }).request).toEqual({ action: 'output' })
    })

    it('keeps a zero timeout, which is a real wait of none', () => {
      expect(pending('get_command_or_subagent_output', { task_ids: ['a'], timeout_ms: 0 }).request).toEqual({ action: 'output', taskId: 'a', timeoutMs: 0 })
    })

    it('states no result before the call finished, and the reason of a failed call', () => {
      expect(pending('kill_command_or_subagent', { task_id: 't' }).result).toBeUndefined()
      expect(ended('kill_command_or_subagent', { task_id: 't' }, 'No such task', { status: 'failed' }).result).toEqual(failedResult('No such task'))
    })
  })

  describe('a scheduler call', () => {
    it('states the schedule and the prompt of a create, with Grok\'s words as the answer', () => {
      const call = ended('scheduler_create', { interval: '5m', prompt: 'Check the build' }, 'Scheduled task sched-1 every 5m.')
      expect(call.kind).toBe('trigger')
      expect(call.request).toEqual({ action: 'create', name: 'Check the build', schedule: '5m' })
      expect(call.result).toEqual(proseResult('Scheduled task sched-1 every 5m.'))
    })

    it('reads the id of a delete from task_id ahead of id', () => {
      expect(pending('scheduler_delete', { task_id: 'a', id: 'b' }).request).toEqual({ action: 'delete', triggerId: 'a' })
      expect(pending('scheduler_delete', { id: 'b' }).request).toEqual({ action: 'delete', triggerId: 'b' })
    })

    it('states a list with no id, and the reason of a failed call', () => {
      expect(pending('scheduler_list', {}).request).toEqual({ action: 'list' })
      expect(ended('scheduler_list', {}, 'Scheduler offline', { status: 'failed' }).result).toEqual(failedResult('Scheduler offline'))
    })
  })

  describe('a web search', () => {
    it('states Grok\'s words as the summary of the search', () => {
      expect(ended('web_search', { query: 'leapmux' }, '1. LeapMux').result).toEqual({ links: [], summary: '1. LeapMux' })
    })

    it('states the reason of a failed search', () => {
      expect(ended('web_search', { query: 'x' }, 'Rate limited', { status: 'failed' }).result).toEqual(failedResult('Rate limited'))
    })
  })

  describe('a shell command', () => {
    it('keeps the shared result when Grok states no record of its own', () => {
      const frame = {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'call',
        status: 'completed',
        kind: 'execute',
        title: 'run_terminal_command',
        rawInput: { command: 'echo hi' },
        content: [{ type: 'content', content: { type: 'text', text: 'hi' } }],
        ...identity('run_terminal_command'),
      }
      const call = acpToolCall(frame, grokToolCallAdapter, undefined)
      if (call.kind !== 'execute' || !call.result || !('commands' in call.result))
        throw new Error('a shell call is an execute row')
      expect(call.result).toEqual(acpToolCall(frame, undefined, undefined).result)
      expect(call.result.commands[0]?.signal).toBeUndefined()
    })

    it('states no result before the command finished', () => {
      expect(pending('run_terminal_command', { command: 'sleep 9' }, { kind: 'execute' }).result).toBeUndefined()
    })
  })

  describe('list_dir and grep', () => {
    it('states the reason of a failed call rather than a listing', () => {
      const call = ended('list_dir', { target_directory: '/p' }, 'Directory not found', { status: 'failed', rawOutput: { type: 'ListDir', Content: { content: '- /p/\n  - a.ts' } } })
      expect(call.result).toEqual(failedResult('Directory not found'))
    })

    it('keeps the words of a listing that is not the tree', () => {
      const call = ended('list_dir', { target_directory: '/p' }, 'Directory not found', { rawOutput: { type: 'ListDir', DirectoryNotFound: { path: '/p' } } })
      expect(call.kind).toBe('list')
      expect(call.result && 'entries' in call.result).toBe(false)
    })

    it('states the reason of a failed grep rather than its matches', () => {
      const call = ended('grep', { pattern: 'x' }, 'Bad pattern', { status: 'failed', rawOutput: { type: 'GrepSearch', match_count: 1, file_matches: [{ path: '/a', matches: [{ content: 'x' }] }] } })
      expect(call.result).toEqual(failedResult('Bad pattern'))
    })
  })

  describe('ask_user_question', () => {
    it('heads the answer with the header of the first question, then its text', () => {
      const withHeader = ended('ask_user_question', { questions: [{ header: 'DB', question: 'Which?', options: [] }] }, '"Which?"="A"')
      expect(withHeader.result).toEqual({ answers: [{ header: 'DB', answer: '"Which?"="A"' }] })
      const withoutHeader = ended('ask_user_question', { questions: [{ question: 'Which?', options: [] }] }, '"Which?"="A"')
      expect(withoutHeader.result).toEqual({ answers: [{ header: 'Which?', answer: '"Which?"="A"' }] })
    })

    it('heads the answer of a call that states no question with a plain word', () => {
      expect(ended('ask_user_question', {}, 'Answered.').result).toEqual({ answers: [{ header: 'Question', answer: 'Answered.' }] })
    })

    it('states no answer before the call finished, nor for an empty answer', () => {
      expect(pending('ask_user_question', { questions: [] }).result).toBeUndefined()
      expect(ended('ask_user_question', { questions: [] }, '').result).toBeUndefined()
    })

    it('states the reason of a failed question', () => {
      expect(ended('ask_user_question', { questions: [] }, 'Dismissed', { status: 'failed' }).result).toEqual(failedResult('Dismissed'))
    })
  })

  describe('todo_write', () => {
    it('states the asked list as the result when Grok states no merged list', () => {
      const call = ended('todo_write', { todos: [{ id: '1', content: 'One', status: 'pending' }] }, '')
      if (call.kind !== 'todo' || !call.result || !('items' in call.result))
        throw new Error('a todo call is a todo row')
      expect(call.result.items.map(item => item.content)).toEqual(['One'])
    })

    it('states no list before the call finished, and the reason of a failed call', () => {
      expect(pending('todo_write', { todos: [] }).result).toBeUndefined()
      expect(ended('todo_write', { todos: [] }, 'Bad list', { status: 'failed' }).result).toEqual(failedResult('Bad list'))
    })

    // A call whose list is no array cannot fill a checklist, so the row keeps the
    // name and draws the call as it came.
    it('draws a call whose todos are no list as the call itself', () => {
      const call = pending('todo_write', { todos: 'one' })
      expect(call.kind).not.toBe('todo')
      expect(call.name).toBe('todo_write')
    })
  })

  describe('spawn_subagent', () => {
    it('titles a launch with no description from a prose title, and with a plain word when the title is the name', () => {
      expect(pending('spawn_subagent', { prompt: 'p' }, { title: 'Find the bug' }).request).toMatchObject({ description: 'Find the bug' })
      expect(pending('spawn_subagent', { prompt: 'p' }).request).toMatchObject({ description: 'Subagent' })
    })

    it('states no run before the launch finished', () => {
      const call = pending('spawn_subagent', { prompt: 'p', description: 'd' })
      expect(call.kind).toBe('agent')
      expect(call.result).toBeUndefined()
    })
  })
})
