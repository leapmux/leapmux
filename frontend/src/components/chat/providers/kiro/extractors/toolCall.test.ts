import { describe, expect, it } from 'vitest'
import { KIRO_TOOL_TITLE } from '~/generated/contracts/kiro-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { failedResult, proseResult, typedResult } from '../../../model/toolCall'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { KIRO_TOOL } from '../toolKinds'
import { kiroToolCallAdapter } from './toolCall'
import '~/components/chat/providers'

/** The opening frame of one Kiro call, as the probe recorded its shape. */
function opening(fields: Record<string, unknown>): Record<string, unknown> {
  return { sessionUpdate: 'tool_call', toolCallId: 'call-1', status: 'in_progress', _meta: { kiro: { toolOrigin: 'default' } }, ...fields }
}

/** The call that one frame draws, alone. */
function callOf(frame: Record<string, unknown>) {
  return providerToolCall(AgentProvider.KIRO, frame)
}

/** The call that one finished frame draws, with its text and any other field Kiro states. */
function finishedCallOf(fields: Record<string, unknown>, text: string, extra: Record<string, unknown> = {}) {
  return callOf({
    ...opening(fields),
    sessionUpdate: 'tool_call_update',
    status: 'completed',
    content: [{ type: 'content', content: { type: 'text', text } }],
    ...extra,
  })
}

/**
 * The call that one frame draws after its turn ended: the worker stores the last
 * frame of the call as the row that closes it, and the row states the end of the
 * turn in its completion alone. No result frame landed.
 */
function endedCallOf(frame: Record<string, unknown>, completion: MessageCompletion) {
  return acpToolCall(frame, kiroToolCallAdapter, undefined, completion)
}

/** The call that one frame draws after the reader stopped its turn. */
function interruptedCallOf(frame: Record<string, unknown>) {
  return endedCallOf(frame, MessageCompletion.INTERRUPTED)
}

/** The opening frame of one subagent spawn. */
function subagentOpening(): Record<string, unknown> {
  return opening({
    title: 'Sub-agent: context-gatherer',
    kind: 'other',
    rawInput: { name: 'context-gatherer', prompt: 'Look.' },
    _meta: { kiro: { kind: 'agent-subtask', agentSubtaskId: 'sub-1' } },
  })
}

/** The opening frame of one question call. */
function questionOpening(): Record<string, unknown> {
  return opening({ title: 'Which database?', kind: 'other', rawInput: {}, _meta: { kiro: { toolId: 'user_input' } } })
}

/** The one run that a subagent call reports, or undefined. */
function runOf(call: ReturnType<typeof acpToolCall> | undefined) {
  return call?.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
}

describe('kiroToolCallAdapter', () => {
  describe('an interrupted call', () => {
    it('shows no list for a to-do call that Kiro never answered', () => {
      const call = interruptedCallOf(opening({ title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'create', tasks: [{ task_description: 'One' }] } }))
      expect(call?.kind).toBe('todo')
      expect(call?.result).toBeUndefined()
    })

    it('shows no stop for a process call that Kiro never answered', () => {
      const call = interruptedCallOf(opening({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop', terminalId: 'term-1' } }))
      expect(call?.kind).toBe('task')
      expect(call?.result).toBeUndefined()
    })

    it('shows no empty answer for an MCP call that the server never answered', () => {
      const call = interruptedCallOf(opening({ title: '@probe/ask', kind: 'other', rawInput: {}, _meta: { kiro: { toolOrigin: 'mcp', serverName: 'probe' } } }))
      expect(call?.kind).toBe('mcp')
      expect(call?.result).toBeUndefined()
    })

    it('shows no finished run for a subagent that never reported', () => {
      const call = interruptedCallOf(subagentOpening())
      expect(call?.kind).toBe('agent')
      expect(runOf(call)?.outcome).toBe('stopped')
      expect(runOf(call)?.body).toBe('')
    })

    it('shows no file content for a read that Kiro never answered', () => {
      const call = interruptedCallOf(opening({ title: KIRO_TOOL.ReadFile, kind: 'read', rawInput: { path: '/w/a.ts' } }))
      expect(call?.kind).toBe('read')
      expect(call?.result).toBeUndefined()
    })

    it('shows no listing for a directory list that Kiro never answered', () => {
      const call = interruptedCallOf(opening({ title: KIRO_TOOL.ListDirectory, kind: 'read', rawInput: { path: '/w' } }))
      expect(call?.result).toBeUndefined()
    })

    it('shows no plan as the answer of a mode switch that Kiro never answered', () => {
      const call = interruptedCallOf(opening({ title: KIRO_TOOL_TITLE.SwitchToExecution, kind: 'switch_mode', rawInput: { plan: '1. Do it.' } }))
      expect(call?.result).toBeUndefined()
    })

    it('keeps the result of a call whose answer landed before the stop', () => {
      const call = interruptedCallOf({
        ...opening({ title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'list' } }),
        sessionUpdate: 'tool_call_update',
        status: 'completed',
        rawOutput: { tasks: [{ id: '1', task_description: 'Kept', completed: false }] },
      })
      expect(call?.kind === 'todo' && typedResult(call)?.items.map(item => item.content)).toEqual(['Kept'])
    })
  })

  describe('a call of a turn that ended with no answer', () => {
    it('states no outcome for a subagent whose turn completed before it reported', () => {
      expect(runOf(endedCallOf(subagentOpening(), MessageCompletion.COMPLETE))?.outcome).toBe('unknown')
    })

    it('states the failure of the turn for a subagent whose turn failed', () => {
      expect(runOf(endedCallOf(subagentOpening(), MessageCompletion.ERROR))?.outcome).toBe('failed')
    })

    it('keeps the report of a subagent that answered', () => {
      const run = runOf(endedCallOf({ ...subagentOpening(), sessionUpdate: 'tool_call_update', status: 'completed', rawOutput: 'Found it.' }, MessageCompletion.COMPLETE))
      expect(run?.outcome).toBe('completed')
      expect(run?.body).toBe('Found it.')
    })

    it('shows no failure for a question that its failed turn left unanswered', () => {
      const call = endedCallOf(questionOpening(), MessageCompletion.ERROR)
      expect(call?.kind).toBe('question')
      expect(call?.result).toBeUndefined()
    })

    it('shows the reason of a question call that Kiro failed', () => {
      const call = endedCallOf({
        ...questionOpening(),
        sessionUpdate: 'tool_call_update',
        status: 'failed',
        content: [{ type: 'content', content: { type: 'text', text: 'No reader answered.' } }],
      }, MessageCompletion.ERROR)
      expect(call?.kind).toBe('question')
      expect(call?.result).toBeDefined()
    })
  })

  describe('a shell command', () => {
    it('reads an execute call as a command whatever its description says', () => {
      // Kiro takes the model's description as the title of a shell call.
      for (const title of [KIRO_TOOL.ListDirectory, KIRO_TOOL.ReadFile, KIRO_TOOL_TITLE.TaskList]) {
        const call = callOf(opening({ toolCallId: 'run_command_t_1', title, kind: 'execute', rawInput: { command: 'ls', description: title } }))
        expect(call?.kind, title).toBe('execute')
      }
    })

    it('reads a shell call titled like the process tool as a command', () => {
      const call = callOf(opening({ toolCallId: 'run_command_t_2', title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { command: 'ls', description: KIRO_TOOL.ControlProcess } }))
      expect(call?.kind).toBe('execute')
    })
  })

  describe('a process call', () => {
    it('draws a start as a background command and a stop as a task', () => {
      expect(callOf(opening({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'start', command: 'npm run dev' } }))?.kind).toBe('execute')
      expect(callOf(opening({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop', terminalId: 'term-1' } }))?.kind).toBe('task')
    })

    it('draws an action that it does not know as the generic card', () => {
      const call = callOf({
        ...opening({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'restart', terminalId: 'term-1' } }),
        sessionUpdate: 'tool_call_update',
        status: 'completed',
        content: [{ type: 'content', content: { type: 'text', text: 'Restarted.' } }],
      })
      expect(call?.kind).not.toBe('task')
      expect(call?.kind).not.toBe('execute')
    })

    it('states the process that a stop acts on, and Kiro\'s words as its output', () => {
      const call = finishedCallOf({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop', terminalId: 'term-1' } }, 'Stopped term-1.')
      expect(call?.kind === 'task' && call.request).toEqual({ action: 'stop', taskId: 'term-1' })
      expect(call?.result).toEqual({ outcome: 'stopped', output: 'Stopped term-1.' })
    })

    it('states a stop with no process id, and the reason of a failed stop', () => {
      expect(callOf(opening({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop' } }))?.request).toEqual({ action: 'stop' })
      const failed = finishedCallOf({ title: KIRO_TOOL.ControlProcess, kind: 'execute', rawInput: { action: 'stop', terminalId: 't' } }, 'No such process', { status: 'failed' })
      expect(failed?.result).toEqual(failedResult('No such process'))
    })
  })

  describe('a shell command with Kiro\'s own record', () => {
    const shell = { toolCallId: 'run_command_t', title: 'List', kind: 'execute', rawInput: { command: 'ls', description: 'List' } }

    it('keeps the shared result when Kiro states neither output nor exit code', () => {
      const frame = { ...opening(shell), sessionUpdate: 'tool_call_update', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'a\nb' } }] }
      expect(acpToolCall(frame, kiroToolCallAdapter, undefined).result).toEqual(acpToolCall(frame, undefined, undefined).result)
    })

    it('states the output alone when Kiro states no exit code', () => {
      const call = finishedCallOf(shell, 'Output:\na\n', { rawOutput: { output: 'a\n' } })
      expect(call?.kind === 'execute' && typedResult(call)?.commands[0]?.output).toBe('a\n')
    })

    it('states the exit code alone when Kiro states no output', () => {
      const call = finishedCallOf(shell, 'Exit Code: 2', { rawOutput: { exitCode: 2 } })
      expect(call?.kind === 'execute' && typedResult(call)?.commands[0]?.exitCode).toBe(2)
    })

    it('states no result before the command finished', () => {
      expect(callOf(opening(shell))?.result).toBeUndefined()
    })
  })

  describe('an MCP call', () => {
    // Kiro's shell call takes the model's description as its title, which can look
    // like a server and a tool.
    it('reads an execute call titled like an MCP tool as a command', () => {
      expect(callOf(opening({ title: '@probe/echo', kind: 'execute', rawInput: { command: 'echo hi' } }))?.kind).toBe('execute')
    })

    it('keeps only the picture URLs that are words', () => {
      const call = finishedCallOf({ title: '@probe/shot', kind: 'other', rawInput: {} }, 'shot', { rawOutput: { imageBase64Urls: ['', 7, null, 'data:image/png;base64,AA=='] } })
      expect(call?.kind === 'mcp' && call.result && 'content' in call.result && call.result.content).toEqual([
        { type: 'text', text: 'shot' },
        { type: 'image', source: { url: 'data:image/png;base64,AA==' } },
      ])
    })

    it('states the reason of a failed server call as its body', () => {
      const call = finishedCallOf({ title: '@probe/echo', kind: 'other', rawInput: {} }, 'Server crashed', { status: 'failed', rawOutput: { imageBase64Urls: ['data:image/png;base64,AA=='] } })
      expect(call?.result).toEqual(failedResult('Server crashed'))
    })
  })

  describe('a file change', () => {
    it('draws an append as an insertion at the end of the file', () => {
      const call = callOf(opening({ title: KIRO_TOOL.AppendToFile, kind: 'edit', rawInput: { path: '/w/a.txt', text: 'more\n' } }))
      expect(call?.kind === 'edit' && call.request.changes[0]).toMatchObject({ filePath: '/w/a.txt', oldStr: '', newStr: 'more\n' })
    })

    it('reads the path of a deletion when it states no target file', () => {
      const call = callOf(opening({ title: KIRO_TOOL.DeleteFile, kind: 'delete', rawInput: { path: '/w/old.txt' } }))
      expect(call?.kind === 'delete' && call.request.changes[0]?.filePath).toBe('/w/old.txt')
    })
  })

  describe('a search', () => {
    it('reads the files that a file search found, with its include pattern', () => {
      const call = finishedCallOf({ title: KIRO_TOOL.FileSearch, kind: 'search', rawInput: { query: 'hello', includePattern: 'src/**' } }, 'You searched for hello and received the following complete results:\n---\nsrc/hello.ts\n---')
      expect(call?.kind).toBe('glob')
      expect(call?.request).toEqual({ pattern: 'hello', paths: ['src/**'] })
      expect(call?.kind === 'glob' && typedResult(call)?.filenames).toEqual(['src/hello.ts'])
    })

    it('states no path for a search with no include pattern', () => {
      expect(callOf(opening({ title: KIRO_TOOL.KnowledgeSearch, kind: 'search', rawInput: { query: 'auth' } }))?.request).toEqual({ pattern: 'auth', paths: [] })
    })

    it('states the reason of a failed search rather than its files', () => {
      const call = finishedCallOf({ title: KIRO_TOOL.FileSearch, kind: 'search', rawInput: { query: 'x' } }, 'You searched for x and received the following complete results:\n---\na\n---', { status: 'failed' })
      expect(call?.result).toEqual(failedResult('You searched for x and received the following complete results:\n---\na\n---'))
    })
  })

  describe('a read', () => {
    it('numbers the lines from one when the call states no offset, and drops the issues Kiro adds', () => {
      const call = finishedCallOf({ title: KIRO_TOOL.ReadFile, kind: 'read', rawInput: { path: '/w/a.ts' } }, '<file name="/w/a.ts">\n<content>\none\ntwo\n</content>\n<issues>\n- unused import\n</issues>\n</file>')
      expect(call?.kind === 'read' && typedResult(call)?.lines).toEqual([{ num: 1, text: 'one' }, { num: 2, text: 'two' }])
    })

    it('keeps the last line of a file that ends with no line break', () => {
      const call = finishedCallOf({ title: KIRO_TOOL.ReadFile, kind: 'read', rawInput: { path: '/w/a.ts', offset: 0 } }, '<file name="/w/a.ts">\n<content>\nlast\n</content>\n</file>')
      expect(call?.kind === 'read' && typedResult(call)?.lines).toEqual([{ num: 1, text: 'last' }])
    })

    it('states the reason of a failed read rather than its lines', () => {
      const text = '<file name="/w/a.ts">\n<content>\none\n</content>\n</file>'
      expect(finishedCallOf({ title: KIRO_TOOL.ReadFile, kind: 'read', rawInput: { path: '/w/a.ts' } }, text, { status: 'failed' })?.result).toEqual(failedResult(text))
    })
  })

  describe('a to-do call', () => {
    it('states the description of the list beside the asked tasks', () => {
      const call = callOf(opening({ title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'create', task_list_description: ' Ship it ', tasks: [{ task_description: 'One' }] } }))
      expect(call?.kind === 'todo' && call.request.note).toBe('Ship it')
      expect(call?.kind === 'todo' && call.request.items.map(item => item.content)).toEqual(['One'])
    })

    it('states the asked tasks as the list when Kiro states no list of its own', () => {
      const call = finishedCallOf({ title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'create', tasks: [{ task_description: 'One' }] } }, 'Created.', { rawOutput: { message: 'Created.' } })
      expect(call?.kind === 'todo' && typedResult(call)?.items.map(item => item.content)).toEqual(['One'])
    })

    it('states the reason of a failed call rather than a list', () => {
      const call = finishedCallOf({ title: KIRO_TOOL_TITLE.TaskList, kind: 'other', rawInput: { command: 'complete' } }, 'No such task', { status: 'failed', rawOutput: { tasks: [] } })
      expect(call?.result).toEqual(failedResult('No such task'))
    })
  })

  describe('a switch to execution', () => {
    it('states no plan for a failed switch or a switch with an empty plan', () => {
      const failed = finishedCallOf({ title: KIRO_TOOL_TITLE.SwitchToExecution, kind: 'other', rawInput: { plan: '1. Do it.' } }, 'Refused', { status: 'failed' })
      expect(failed?.result).toEqual(failedResult('Refused'))
      expect(failed?.title).toBe('Switch to execution')
      const empty = finishedCallOf({ title: KIRO_TOOL_TITLE.SwitchToExecution, kind: 'other', rawInput: { plan: '   ' } }, 'Switched.')
      expect(empty?.kind).toBe('switch_mode')
      expect(empty?.result).not.toEqual(proseResult('   ', 'markdown'))
      expect(empty?.result).not.toEqual(proseResult('', 'markdown'))
    })
  })

  describe('a report and a memory call', () => {
    it('states the arguments as the payload and Kiro\'s words as the answer', () => {
      const report = finishedCallOf({ title: KIRO_TOOL.ReportProgress, kind: 'other', rawInput: { progress: 'Half done' } }, 'Progress noted.')
      expect(report?.kind === 'report' && report.request).toEqual({ payload: { progress: 'Half done' } })
      expect(report?.result).toEqual(proseResult('Progress noted.', 'markdown'))
      const memory = finishedCallOf({ title: KIRO_TOOL.Memory, kind: 'other', rawInput: { command: 'view' } }, 'No memories.')
      expect(memory?.kind === 'memory' && memory.request).toEqual({ payload: { command: 'view' } })
      expect(memory?.result).toEqual(proseResult('No memories.'))
    })

    // An empty payload is no payload, as the shared request of each kind states it.
    it('states no payload for a call with no arguments', () => {
      expect(callOf(opening({ title: KIRO_TOOL.UpdateSessionInformation, kind: 'other', rawInput: {} }))?.request).toEqual({})
    })

    it('states the reason of a failed call', () => {
      expect(finishedCallOf({ title: KIRO_TOOL.Memory, kind: 'other', rawInput: { command: 'view' } }, 'Store locked', { status: 'failed' })?.result).toEqual(failedResult('Store locked'))
    })
  })

  describe('a title that Kiro does not list', () => {
    it('keeps the title as the name of the call', () => {
      expect(callOf(opening({ title: 'A Tool From A Later Release', kind: 'other', rawInput: { a: 1 } }))?.name).toBe('A Tool From A Later Release')
    })

    it('keeps the wire kind as the name of a call with no title', () => {
      expect(callOf(opening({ kind: 'fetch', rawInput: { url: 'https://example.com' } }))?.name).toBe('fetch')
    })
  })

  describe('a question call', () => {
    // The sub-option pages are part of the dialog, not of the question the model asked.
    it('states the question alone, never the pages of its sub-options', () => {
      const call = callOf(opening({ title: 'Which DB?', kind: 'other', _meta: { kiro: { toolId: 'user_input', userInputOptions: [{ title: 'Postgres', subOptions: [{ title: 'PostGIS' }] }] } } }))
      expect(call?.kind === 'question' && call.request.questions).toEqual([{ question: 'Which DB?', options: [{ value: 'Postgres', label: 'Postgres' }], multiSelect: false }])
      expect(call?.title).toBe('Question')
    })
  })
})
