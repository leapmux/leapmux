import type { ToolCall } from '../../../model/toolCall'
import type { CodewhaleToolFacts } from './toolCall'
import type { ProviderRowOptions } from '~/test-support/toolCallFixture'
import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_EVENT, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE, CODEWHALE_WORKFLOW_STATUS } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { CALL, childBlock, codewhaleEvent, requestSide, toolCompleted, toolFailed, toolFinished, toolStarted } from '../toolResults.fixtures'
import { CODEWHALE_TOOL_READERS } from './toolCall'
import '~/components/chat/providers'

function call(payload: Record<string, unknown>, options: ProviderRowOptions = {}): ToolCall {
  const built = providerToolCall(AgentProvider.CODEWHALE, payload, options)
  if (!built)
    throw new Error('the frame drew no tool row')
  return built
}

/** A finished call read from its result row, with its opening frame beside it. */
function finished(name: string, args: Record<string, unknown>, detail: string, metadata: Record<string, unknown> = {}): ToolCall {
  return call(toolCompleted(name, args, detail, metadata), { spanType: name, request: requestSide(toolStarted(name, args)) })
}

describe('codewhaleToolCall', () => {
  describe('a command', () => {
    it('states the command and no result while it runs', () => {
      const running = call(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls -1' }), { role: 'request' })
      expect(running.kind).toBe('execute')
      expect(running.status).toBe('in_progress')
      expect(running.request).toStrictEqual({ command: 'ls -1' })
      expect(running.result).toBeUndefined()
      expect(running.label).toBe(CODEWHALE_TOOL.Bash)
    })

    it('reads the output, the exit code and the duration of a finished command', () => {
      const done = finished(CODEWHALE_TOOL.Bash, { command: 'ls -1' }, 'a.ts\n', { exit_code: 0, duration_ms: 35 })
      expect(done.status).toBe('completed')
      expect(done.result).toStrictEqual({ commands: [{ output: 'a.ts\n', exitCode: 0, durationMs: 35 }], unresolvedTerminals: [] })
    })

    it('keeps the output of a command that ran and failed', () => {
      const failing = call(toolFinished(CODEWHALE_EVENT.ItemFailed, CODEWHALE_TOOL.Bash, { command: 'false' }, 'boom', { exit_code: 2 }), { spanType: CODEWHALE_TOOL.Bash })
      expect(failing.status).toBe('failed')
      expect(failing.result).toStrictEqual({ commands: [{ output: 'boom', exitCode: 2 }], unresolvedTerminals: [] })
    })

    it('states the reason of a command that never ran', () => {
      const denied = call(toolFailed(CODEWHALE_TOOL.Bash, { command: 'touch x' }, 'Tool \'bash\' denied by user'), { spanType: CODEWHALE_TOOL.Bash })
      expect(denied.status).toBe('failed')
      expect(denied.result).toStrictEqual({ failure: true, text: 'Tool \'bash\' denied by user' })
      // The final event repeats the arguments, so the row keeps its command.
      expect(denied.request).toStrictEqual({ command: 'touch x' })
    })

    it('reads a stopped command as cancelled', () => {
      const stopped = call(toolFinished(CODEWHALE_EVENT.ItemInterrupted, CODEWHALE_TOOL.Bash, { command: 'sleep 9' }, 'Killed'), { spanType: CODEWHALE_TOOL.Bash })
      expect(stopped.status).toBe('cancelled')
      const withdrawn = call(toolFinished(CODEWHALE_EVENT.ItemCanceled, CODEWHALE_TOOL.Bash, { command: 'sleep 9' }, ''), { spanType: CODEWHALE_TOOL.Bash })
      expect(withdrawn.status).toBe('cancelled')
    })

    it('reads a retained opening frame as a call its turn ended', () => {
      const retained = providerToolCall(AgentProvider.CODEWHALE, toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 9' }), { completion: MessageCompletion.INTERRUPTED })
      expect(retained?.status).toBe('cancelled')
      expect(retained?.result).toBeUndefined()
    })

    // A background launch answers at once with a null code, because the job it
    // started has not ended. The words are the launch notice, not a failure.
    it('draws a background launch, whose exit code is null, as output with no code', () => {
      const launched = finished(CODEWHALE_TOOL.TaskShellStart, { command: 'npm run dev' }, 'Background task started: shell_1', { task_id: 'shell_1', backgrounded: true, status: 'Running', exit_code: null })
      expect(launched.status).toBe('completed')
      expect(launched.result).toStrictEqual({ commands: [{ output: 'Background task started: shell_1' }], unresolvedTerminals: [] })
    })

    it('composes the command of a tool that states none', () => {
      expect(finished(CODEWHALE_TOOL.GitStatus, {}, 'clean', { exit_code: 0 }).request).toStrictEqual({ command: 'git status' })
      expect(finished(CODEWHALE_TOOL.Git, { action: 'commit_plan', path: 'src' }, 'x', { exit_code: 0 }).request).toStrictEqual({ command: 'git commit-plan src' })
      expect(finished(CODEWHALE_TOOL.RunTests, { args: '--lib' }, 'ok', { exit_code: 0 }).request).toStrictEqual({ command: 'cargo test --lib' })
      expect(finished(CODEWHALE_TOOL.RunVerifiers, {}, 'ok').request).toStrictEqual({ command: CODEWHALE_TOOL.RunVerifiers })
      expect(finished(CODEWHALE_TOOL.JsExecution, { code: '1 + 1' }, '2').request).toStrictEqual({ command: '1 + 1', language: 'javascript' })
    })
  })

  describe('a file change', () => {
    const DIFF = '--- a/a.ts\n+++ b/a.ts\n@@ -1 +1 @@\n-before\n+after\n'
    const MUTATION = { mutation: { diff: DIFF, files: [{ path: 'a.ts', outcome: 'updated' }], renames: [] } }

    it('states one change for each substitution while it runs', () => {
      const running = call(toolStarted(CODEWHALE_TOOL.Edit, { path: 'a.ts', edits: [{ oldText: 'a', newText: 'b' }, { oldText: 'c', newText: 'd' }] }), { role: 'request' })
      expect(running.kind).toBe('edit')
      expect(running.request).toStrictEqual({
        changes: [
          { filePath: 'a.ts', structuredPatch: null, oldStr: 'a', newStr: 'b' },
          { filePath: 'a.ts', structuredPatch: null, oldStr: 'c', newStr: 'd' },
        ],
      })
    })

    it('states the landed diff once the call answers', () => {
      const done = finished(CODEWHALE_TOOL.Edit, { path: 'a.ts', edits: [{ oldText: 'before', newText: 'after' }] }, 'Replaced 1 block', MUTATION)
      expect(done.status).toBe('completed')
      const result = done.result as { changes: Array<{ filePath: string, operation?: string, structuredPatch?: unknown }> }
      expect(result.changes).toHaveLength(1)
      expect(result.changes[0]?.filePath).toBe('a.ts')
      expect(result.changes[0]?.operation).toBe('edit')
      expect(result.changes[0]?.structuredPatch).toStrictEqual([{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-before', '+after'] }])
    })

    it('states a written file as an addition', () => {
      const done = finished(CODEWHALE_TOOL.Write, { path: 'n.txt', content: 'hi\n' }, 'Wrote', {
        mutation: { diff: '--- a/n.txt\n+++ b/n.txt\n@@ -0,0 +1 @@\n+hi\n', files: [{ path: 'n.txt', outcome: 'created' }], renames: [] },
      })
      expect(done.kind).toBe('write')
      expect((done.result as { changes: Array<{ operation?: string }> }).changes[0]?.operation).toBe('add')
    })

    it('states the requested change when the landed record has no hunks this build can place', () => {
      const done = finished(CODEWHALE_TOOL.Edit, { path: 'a.ts', edits: [{ oldText: 'x', newText: 'y' }] }, 'Replaced', { mutation: { diff: '', files: [{ path: 'a.ts', outcome: 'updated' }], renames: [] } })
      expect(done.result).toStrictEqual({ changes: [{ filePath: 'a.ts', structuredPatch: null, oldStr: 'x', newStr: 'y' }] })
    })

    it('falls back to the requested change when the result states no mutation', () => {
      const done = finished(CODEWHALE_TOOL.WriteFile, { path: 'n.txt', content: 'hi' }, 'Wrote')
      expect(done.result).toStrictEqual({ changes: [{ filePath: 'n.txt', structuredPatch: null, oldStr: '', newStr: 'hi', operation: 'add' }] })
    })

    it('reads the arguments of a failed change and keeps the file in the row', () => {
      const failing = call(toolFailed(CODEWHALE_TOOL.ApplyPatch, { patch: DIFF }, 'Patch must include a hunk header'), { spanType: CODEWHALE_TOOL.ApplyPatch })
      expect(failing.kind).toBe('edit')
      expect(failing.status).toBe('failed')
      expect((failing.request as { changes: Array<{ filePath: string }> }).changes.map(change => change.filePath)).toStrictEqual(['a.ts'])
    })

    it('takes the generic card for a change that states no file', () => {
      expect(finished(CODEWHALE_TOOL.Edit, { edits: [{ oldText: 'a', newText: 'b' }] }, 'x').kind).toBe('other')
    })

    // The landed record names the file the arguments left out, so the row is still
    // a file change, and both sides state the change that landed.
    it('keeps a change whose arguments name no file when the landed record names one', () => {
      const done = finished(CODEWHALE_TOOL.Edit, { edits: [{ oldText: 'before', newText: 'after' }] }, 'Replaced 1 block', MUTATION)
      expect(done.kind).toBe('edit')
      expect((done.request as { changes: Array<{ filePath: string }> }).changes.map(change => change.filePath)).toStrictEqual(['a.ts'])
      expect((done.result as { changes: Array<{ filePath: string }> }).changes.map(change => change.filePath)).toStrictEqual(['a.ts'])
    })

    it('states the failure words of a change that failed, and the shared sentence when it gave none', () => {
      const failing = call(toolFailed(CODEWHALE_TOOL.Write, { path: 'a.ts', content: 'x' }, 'Permission denied'), { spanType: CODEWHALE_TOOL.Write })
      expect(failing.result).toStrictEqual({ failure: true, text: 'Permission denied' })
      const silent = call(toolFailed(CODEWHALE_TOOL.Write, { path: 'a.ts', content: 'x' }, ''), { spanType: CODEWHALE_TOOL.Write })
      expect(silent.result).toStrictEqual({ failure: true, text: 'Tool call failed' })
    })

    it('reads the operation of the File facade from its action', () => {
      expect(finished(CODEWHALE_TOOL.File, { action: 'list', path: '.' }, '[]').kind).toBe('list')
      expect(finished(CODEWHALE_TOOL.File, { action: 'search_name', query: 'a' }, '[]').kind).toBe('glob')
      expect(finished(CODEWHALE_TOOL.File, { action: 'search_content', pattern: 'a' }, '{"matches":[]}').kind).toBe('grep')
      expect(finished(CODEWHALE_TOOL.File, { action: 'write', path: 'a.ts', content: 'x' }, 'Wrote').kind).toBe('write')
      expect(finished(CODEWHALE_TOOL.File, { action: 'patch', patch: DIFF }, 'Applied').kind).toBe('edit')
      expect(finished(CODEWHALE_TOOL.File, { action: 'a_later_action', path: 'a.ts' }, 'x').kind).toBe('read')
    })
  })

  describe('the reads and the searches', () => {
    it('numbers a read from the offset it asked for', () => {
      const done = finished(CODEWHALE_TOOL.Read, { path: 'a.ts', offset: 10 }, 'ten\neleven')
      expect(done.request).toStrictEqual({ path: 'a.ts', offset: 10 })
      expect(done.result).toStrictEqual({ lines: [{ num: 10, text: 'ten' }, { num: 11, text: 'eleven' }], fallbackContent: 'ten\neleven' })
    })

    it('reads a grep answer into matches', () => {
      const done = finished(CODEWHALE_TOOL.GrepFiles, { pattern: 'x' }, JSON.stringify({ matches: [{ file: 'a.ts', line_number: 2, line: 'x: 1' }], total_matches: 1, truncated: false }))
      expect(done.kind).toBe('grep')
      expect(done.request).toStrictEqual({ pattern: 'x', paths: [] })
      expect((done.result as { lines: unknown[] }).lines).toStrictEqual([{ filePath: 'a.ts', lineNumber: 2, text: 'x: 1' }])
    })

    it('keeps a search answer it cannot read as the text the tool printed', () => {
      const done = finished(CODEWHALE_TOOL.GrepFiles, { pattern: 'x' }, 'not json')
      expect(done.result).toStrictEqual({ unparsed: true, text: 'not json' })
    })

    it('reads the query of every web search spelling', () => {
      expect(finished(CODEWHALE_TOOL.WebSearch, { q: 'one' }, 'r').request).toStrictEqual({ query: 'one' })
      expect(finished(CODEWHALE_TOOL.WebRun, { search_query: [{ query: 'two' }] }, 'r').request).toStrictEqual({ query: 'two' })
    })

    it('reads the operation of the Web facade from its action', () => {
      expect(finished(CODEWHALE_TOOL.Web, { action: 'fetch', url: 'https://example.com' }, 'page').kind).toBe('fetch')
      expect(finished(CODEWHALE_TOOL.Web, { action: 'wait', url: 'http://localhost' }, 'ready').kind).toBe('wait')
    })

    it('keeps the Web facade a search for an action it does not know', () => {
      const done = finished(CODEWHALE_TOOL.Web, { action: 'a_later_action', query: 'q' }, 'found')
      expect(done.kind).toBe('web_search')
      expect(done.request).toStrictEqual({ query: 'q' })
      expect(done.result).toStrictEqual({ links: [], summary: 'found' })
    })

    it('reads the first query record of an advanced search, and no query from a list in another shape', () => {
      expect(finished(CODEWHALE_TOOL.WebRun, { search_query: ['x', { q: 'two' }, { q: 'three' }] }, 'r').request).toStrictEqual({ query: 'two' })
      expect(finished(CODEWHALE_TOOL.WebRun, { search_query: 'one' }, 'r').request).toStrictEqual({ query: '' })
      expect(finished(CODEWHALE_TOOL.WebSearch, { query: 'direct', search_query: [{ q: 'advanced' }] }, 'r').request).toStrictEqual({ query: 'direct' })
    })

    it('keeps a file-name search and a listing it cannot read as the text the tool printed', () => {
      expect(finished(CODEWHALE_TOOL.FileSearch, { query: 'a' }, 'No index yet').result).toStrictEqual({ unparsed: true, text: 'No index yet' })
      expect(finished(CODEWHALE_TOOL.ListDir, { path: '.' }, 'Permission denied').result).toStrictEqual({ unparsed: true, text: 'Permission denied' })
    })

    it('reads the duration of a fetch only when it is a number', () => {
      expect(finished(CODEWHALE_TOOL.FetchURL, { url: 'https://example.com' }, 'page', { duration_ms: 40 }).result).toStrictEqual({ result: 'page', durationMs: 40 })
      expect(finished(CODEWHALE_TOOL.FetchURL, { url: 'https://example.com' }, 'page', { duration_ms: '40' }).result).toStrictEqual({ result: 'page' })
    })
  })

  describe('a subagent', () => {
    it('states the launched child with its prompt', () => {
      const done = finished(CODEWHALE_TOOL.Agent, { action: 'start', prompt: 'Count the files.', type: 'explore', name: 'counter' }, JSON.stringify({ name: 'counter', agent_id: 'agent_1', status: 'running' }))
      expect(done.title).toBe('counter')
      expect(done.request).toStrictEqual({ description: 'counter', agentType: 'explore', prompt: 'Count the files.' })
      expect(done.result).toStrictEqual({
        agents: [{
          description: 'counter',
          agentId: 'agent_1',
          statusLabel: 'running',
          outcome: 'running',
          metadata: [{ label: 'Agent ID', value: 'agent_1' }],
          body: 'Count the files.',
          bodyLabel: 'Prompt',
        }],
      })
    })

    it('states the children a wait settled', () => {
      const done = finished(CODEWHALE_TOOL.Agent, { action: 'wait' }, JSON.stringify({ action: 'wait', settled: [{ agent_id: 'agent_1', name: 'counter', status: 'completed' }], running: 0, note: 'Read the results.' }))
      expect(done.request).toStrictEqual({ description: 'wait', prompt: '' })
      expect((done.result as { agents: Array<{ outcome: string, body: string }> }).agents).toMatchObject([{ outcome: 'completed', body: 'Read the results.' }])
    })

    it('keeps an answer that names no child as text', () => {
      expect(finished(CODEWHALE_TOOL.Agent, { action: 'wait' }, JSON.stringify({ settled: [], note: 'Nothing ran.' })).result).toStrictEqual({ unparsed: true, text: JSON.stringify({ settled: [], note: 'Nothing ran.' }) })
    })

    // A start that names neither the child nor its type has no description of its
    // own, so the header takes the call's description, and then the tool's name.
    it('titles a start that names no child by the call\'s description, then by the tool', () => {
      const answer = JSON.stringify({ agent_id: 'agent_1', status: 'running' })
      expect(finished(CODEWHALE_TOOL.Agent, { prompt: 'Go.', description: 'Count the files' }, answer).title).toBe('Count the files')
      expect(finished(CODEWHALE_TOOL.Agent, { prompt: 'Go.' }, answer).title).toBe(CODEWHALE_TOOL.Agent)
    })
  })

  describe('the checklists', () => {
    it('states the runtime\'s own checklist once the call lands', () => {
      const done = finished(CODEWHALE_TOOL.TodoWrite, { todos: [{ content: 'A', status: 'pending' }] }, 'updated', { task_updates: { checklist: { items: [{ id: 1, content: 'A', status: 'in_progress' }] } } })
      expect(done.kind).toBe('todo')
      expect((done.result as { items: Array<{ content: string, status: string }> }).items).toMatchObject([{ content: 'A', status: 'in_progress' }])
    })

    it('reads a plan\'s steps and its explanation', () => {
      const done = finished(CODEWHALE_TOOL.UpdatePlan, { explanation: 'Why', plan: [{ step: 'Look', status: 'completed' }] }, 'Plan updated')
      expect(done.request).toMatchObject({ items: [{ content: 'Look', status: 'completed' }], note: 'Why' })
      // The runtime keeps no checklist for a plan, so the result states the plan's own
      // steps, with the explanation above them.
      expect(done.result).toMatchObject({ items: [{ content: 'Look', status: 'completed' }], note: 'Why' })
    })

    it('states no note for a plan whose explanation is blank', () => {
      const done = finished(CODEWHALE_TOOL.UpdatePlan, { explanation: '  ', plan: [{ step: 'Look', status: 'pending' }] }, 'Plan updated')
      expect(done.request).not.toHaveProperty('note')
      expect(done.result).not.toHaveProperty('note')
    })

    it('draws the request\'s list for a checklist call whose result keeps none', () => {
      const done = finished(CODEWHALE_TOOL.TodoWrite, { todos: [{ content: 'A', status: 'pending' }] }, 'updated', { task_updates: {} })
      expect((done.result as { items: Array<{ content: string, status: string }> }).items).toMatchObject([{ content: 'A', status: 'pending' }])
    })

    it('takes the generic card for a list-less call', () => {
      expect(finished(CODEWHALE_TOOL.TodoWrite, {}, 'x').kind).toBe('other')
    })
  })

  describe('the rest of the vocabulary', () => {
    // Read from the REQUEST row: the result row of an answered question hides,
    // because the runtime redacts the answers from it.
    it('reads the questions and states no redacted answer', () => {
      const args = { questions: [{ id: 'color', header: 'Color', question: 'Which color?', options: [{ label: 'Red' }] }] }
      const done = call(toolStarted(CODEWHALE_TOOL.RequestUserInput, args), {
        role: 'request',
        spanType: CODEWHALE_TOOL.RequestUserInput,
        result: requestSide(toolCompleted(CODEWHALE_TOOL.RequestUserInput, args, 'User input submitted')),
      })
      expect(done.request).toStrictEqual({ questions: [{ header: 'Color', question: 'Which color?', options: [{ label: 'Red' }] }] })
      expect(done.result).toStrictEqual({ answers: [] })
      expect(done.status).toBe('completed')
    })

    it('reads the server and the tool of a Model Context Protocol call', () => {
      const done = finished('mcp_docs_search', { q: 'x' }, 'found')
      expect(done.kind).toBe('mcp')
      expect(done.request).toStrictEqual({ args: { q: 'x' }, server: 'docs', tool: 'search' })
      expect(done.result).toStrictEqual({ content: [{ type: 'text', text: 'found' }] })
    })

    it('reads a workflow run\'s status as the task outcome', () => {
      const outcomes: Array<[string, string]> = [
        [CODEWHALE_WORKFLOW_STATUS.Running, 'running'],
        [CODEWHALE_WORKFLOW_STATUS.Completed, 'completed'],
        [CODEWHALE_WORKFLOW_STATUS.Degraded, 'failed'],
        [CODEWHALE_WORKFLOW_STATUS.Failed, 'failed'],
        [CODEWHALE_WORKFLOW_STATUS.Cancelled, 'stopped'],
        ['a_later_word', 'running'],
      ]
      for (const [status, outcome] of outcomes)
        expect(finished(CODEWHALE_TOOL.Workflow, { action: 'status', run_id: 'r1' }, 'ok', { status }).result, status).toMatchObject({ outcome })
      // A workflow result that states no status is a call that finished.
      expect(finished(CODEWHALE_TOOL.Workflow, { action: 'start' }, 'ok').result).toMatchObject({ outcome: 'completed' })
      expect(finished(CODEWHALE_TOOL.TaskShellWait, { task_id: 's1' }, 'exited').request).toStrictEqual({ action: 'output', taskId: 's1' })
      expect(finished(CODEWHALE_TOOL.Tasks, { action: 'cancel', id: 't1' }, 'x').request).toStrictEqual({ action: 'stop', taskId: 't1' })
    })

    it('states a notice to the reader as its title and body', () => {
      expect(finished(CODEWHALE_TOOL.Notify, { title: 'Done', body: 'Built.' }, 'ok').request).toStrictEqual({ text: 'Done\nBuilt.' })
      expect(finished(CODEWHALE_TOOL.Notify, { body: 'Built.' }, 'ok').request).toStrictEqual({ text: 'Built.' })
    })

    it('states the child a message goes to, and its words from each spelling', () => {
      expect(finished(CODEWHALE_TOOL.AgentsFollowup, { agent_id: 'agent_1', message: 'Also count dirs.' }, 'Delivered').request).toStrictEqual({ to: 'agent_1', text: 'Also count dirs.' })
      expect(finished(CODEWHALE_TOOL.AgentsMessage, { to: 'agent_2', prompt: 'Stop soon.' }, 'Delivered').request).toStrictEqual({ to: 'agent_2', text: 'Stop soon.' })
      expect(finished(CODEWHALE_TOOL.AgentsMessage, { agent_id: 'agent_1', to: 'agent_2', text: 'Hi.' }, 'Delivered').request).toStrictEqual({ to: 'agent_1', text: 'Hi.' })
      expect(finished(CODEWHALE_TOOL.AgentsMessage, {}, 'Delivered').request).toStrictEqual({ text: '' })
    })

    it('reads the action and the target of each background-task call', () => {
      const cases: Array<[string, Record<string, unknown>, Record<string, unknown>]> = [
        [CODEWHALE_TOOL.TerminalCancel, { terminal_id: 't1' }, { action: 'stop', taskId: 't1' }],
        [CODEWHALE_TOOL.TerminalWait, { terminal_id: 't1' }, { action: 'output', taskId: 't1' }],
        // The tool name states the action, whatever the arguments say.
        [CODEWHALE_TOOL.TaskShellWait, { task_id: 's1', action: 'cancel' }, { action: 'output', taskId: 's1' }],
        [CODEWHALE_TOOL.Workflow, { action: 'status', run_id: 'r1' }, { action: 'output', taskId: 'r1' }],
        [CODEWHALE_TOOL.Tasks, { action: 'list' }, { action: 'list' }],
        [CODEWHALE_TOOL.Tasks, { action: 'read', id: 't1', task_id: 't0' }, { action: 'output', taskId: 't0' }],
        [CODEWHALE_TOOL.StartMcpServer, { name: 'docs' }, { action: 'other', taskId: 'docs' }],
        [CODEWHALE_TOOL.TerminalReset, {}, { action: 'other' }],
        [CODEWHALE_TOOL.Tasks, { action: 'a_later_action' }, { action: 'other' }],
      ]
      for (const [name, args, request] of cases)
        expect(finished(name, args, 'ok').request, `${name} ${JSON.stringify(args)}`).toStrictEqual(request)
    })

    it('titles a prose call by its description, then by the tool', () => {
      expect(finished(CODEWHALE_TOOL.Review, { description: 'Review the branch', target: 'HEAD' }, 'No findings').title).toBe('Review the branch')
      expect(finished(CODEWHALE_TOOL.Review, { target: 'HEAD' }, 'No findings').title).toBe(CODEWHALE_TOOL.Review)
    })

    it('states a prose call\'s words as plain text', () => {
      expect(finished(CODEWHALE_TOOL.Remember, { note: 'Use bun.' }, 'Remembered').result).toStrictEqual({ text: 'Remembered', format: 'plain' })
    })

    // A call that the turn cut short ended without its answer, so its words read as
    // the reason it stopped, the same as a failure's.
    it('states a prose call that the turn cut short as cancelled, with its words as the reason', () => {
      const stopped = call(toolFinished(CODEWHALE_EVENT.ItemInterrupted, CODEWHALE_TOOL.Review, {}, 'Stopped by the reader'), { spanType: CODEWHALE_TOOL.Review })
      expect(stopped.status).toBe('cancelled')
      expect(stopped.result).toStrictEqual({ failure: true, text: 'Stopped by the reader' })
    })

    it('takes the generic card for a tool no table lists', () => {
      const done = finished('a_tool_from_a_later_release', { a: 1 }, 'x')
      expect(done.kind).toBe('other')
      expect(done.label).toBe('a_tool_from_a_later_release')
      expect(done.result).toStrictEqual({ content: [{ type: 'text', text: 'x' }] })
    })

    it('states no content on the generic card for a call that answered nothing, and the failure for one that failed', () => {
      expect(finished('a_tool_from_a_later_release', {}, '').result).toStrictEqual({ content: [] })
      const failing = call(toolFailed('mcp_docs_search', { q: 'x' }, 'Server gone'), { spanType: 'mcp_docs_search' })
      expect(failing.kind).toBe('mcp')
      expect(failing.status).toBe('failed')
      expect(failing.result).toStrictEqual({ failure: true, text: 'Server gone' })
    })
  })

  describe('the pairing', () => {
    it('ignores a paired row of another call', () => {
      const request = requestSide(toolStarted(CODEWHALE_TOOL.Read, { path: 'other.ts' }, 'call-2'))
      const done = call(toolCompleted(CODEWHALE_TOOL.Read, {}, 'x'), { spanType: CODEWHALE_TOOL.Read, request })
      expect(done.request).toStrictEqual({ path: '' })
    })

    it('draws the whole call on the request row once the result lands', () => {
      const result = requestSide(toolCompleted(CODEWHALE_TOOL.Bash, { command: 'ls' }, 'a.ts', { exit_code: 0 }))
      const merged = call(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }), { role: 'request', result })
      expect(merged.status).toBe('completed')
      expect(merged.result).toStrictEqual({ commands: [{ output: 'a.ts', exitCode: 0 }], unresolvedTerminals: [] })
    })

    it('reads a subagent\'s tool blocks, naming the tool from the span', () => {
      const use = childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: CALL, name: CODEWHALE_TOOL.Bash, input: { command: 'ls' } })
      const result = childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: CALL, content: 'a.ts' }, 2)
      const done = call(result, { spanType: CODEWHALE_TOOL.Bash, request: requestSide(use) })
      expect(done.kind).toBe('execute')
      expect(done.request).toStrictEqual({ command: 'ls' })
      expect(done.result).toStrictEqual({ commands: [{ output: 'a.ts' }], unresolvedTerminals: [] })
      const failed = call(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: CALL, content: 'no', is_error: true }, 2), { spanType: CODEWHALE_TOOL.Read, request: requestSide(childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: CALL, name: CODEWHALE_TOOL.Read, input: { path: 'x' } })) })
      expect(failed.status).toBe('failed')
    })

    it('reads a subagent result whose content is a block list', () => {
      const result = childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: CALL, content: [{ type: 'text', text: 'one' }, { type: 'image' }, { type: 'text', text: 'two' }] }, 2)
      expect(call(result, { spanType: CODEWHALE_TOOL.WebSearch }).result).toStrictEqual({ links: [], summary: 'one\ntwo' })
    })

    it('reads no tool frame from an item that names no call', () => {
      const unnamed = codewhaleEvent(CODEWHALE_EVENT.ItemCompleted, { item: { kind: 'tool_call', detail: 'x', metadata: { tool_name: CODEWHALE_TOOL.Bash } } })
      expect(providerToolCall(AgentProvider.CODEWHALE, unnamed)).toBeNull()
    })

    // A subagent's result block states no tool, and here no request row and no span
    // name supplies one. The row takes the generic card and invents no label.
    it('takes the generic card and no label for a result that no source names', () => {
      const orphan = call(childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: CALL, content: 'x' }, 2))
      expect(orphan.kind).toBe('other')
      expect(orphan.label).toBeUndefined()
      expect(orphan.result).toStrictEqual({ content: [{ type: 'text', text: 'x' }] })
    })

    it('takes the tool name from the request row over the span name', () => {
      const use = childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.ToolUse, id: CALL, name: CODEWHALE_TOOL.Read, input: { path: 'a.ts' } })
      const result = childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: CALL, content: 'alpha' }, 2)
      const done = call(result, { spanType: CODEWHALE_TOOL.Bash, request: requestSide(use) })
      expect(done.kind).toBe('read')
      expect(done.label).toBe(CODEWHALE_TOOL.Read)
    })

    // A request row whose result side holds another OPENING frame has no answer yet:
    // only a final frame answers the call.
    it('reads a paired opening frame as no answer', () => {
      const running = call(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }), { role: 'request', result: requestSide(toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' })) })
      expect(running.status).toBe('in_progress')
      expect(running.result).toBeUndefined()
    })
  })
})

describe('CODEWHALE_TOOL_READERS', () => {
  /** The facts of one call, with every field a reader may take stated. */
  function facts(overrides: Partial<CodewhaleToolFacts>): CodewhaleToolFacts {
    return {
      toolName: 'a_tool',
      kind: 'think',
      input: {},
      resultFrame: null,
      resultAvailable: false,
      failed: false,
      interrupted: false,
      text: '',
      metadata: {},
      requestedChanges: [],
      landedChanges: null,
      ...overrides,
    }
  }

  // No Codewhale tool takes these kinds today, and the table must still read one
  // correctly if a later tool does.
  it('reads a kind no tool takes as its request and the words the call printed', () => {
    for (const kind of ['chart', 'delete', 'image', 'move', 'switch_mode', 'think'] as const) {
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind })), kind).toMatchObject({ kind, title: 'a_tool' })
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind })), kind).not.toHaveProperty('result')
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind, resultAvailable: true, text: 'out' })).result, kind).toStrictEqual({ unparsed: true, text: 'out' })
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind, resultAvailable: true })), kind).not.toHaveProperty('result')
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind, resultAvailable: true, failed: true, text: 'no' })).result, kind).toStrictEqual({ failure: true, text: 'no' })
      expect(CODEWHALE_TOOL_READERS[kind](facts({ kind, resultAvailable: true, interrupted: true })).result, kind).toStrictEqual({ failure: true, text: 'Tool call failed' })
    }
  })

  it('reads the unspecified kind as the generic card', () => {
    expect(CODEWHALE_TOOL_READERS.unspecified(facts({ kind: 'unspecified', toolName: '', input: { a: 1 }, resultAvailable: true, text: 'x' })))
      .toStrictEqual({ kind: 'unspecified', request: { args: { a: 1 } }, result: { content: [{ type: 'text', text: 'x' }] } })
  })

  // `codewhaleReclassify` answers `other` for a file row that names no file, so this
  // branch is the table's own fallback for a row that reaches it anyway.
  it('states the printed words for a file change that states no change on either side', () => {
    expect(CODEWHALE_TOOL_READERS.edit(facts({ kind: 'edit', resultAvailable: true, text: 'Applied' })))
      .toStrictEqual({ kind: 'edit', request: { changes: [] }, result: { unparsed: true, text: 'Applied' } })
  })
})
