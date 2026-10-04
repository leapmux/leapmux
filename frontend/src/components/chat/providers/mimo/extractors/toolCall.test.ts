import type { ToolCall } from '../../../model/toolCall'
import type { ProviderRowOptions } from '~/test-support/toolCallFixture'
import { describe, expect, it } from 'vitest'
import { typedResult } from '~/components/chat/model/toolCall'
import { MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openingFrame, parsedFrame, toolFrame } from '~/test-support/mimoFixtures'
import { providerRow, providerToolCall } from '~/test-support/toolCallFixture'
import '~/components/chat/providers'

/** The call one finished frame draws, beside its opening frame. */
function finished(tool: string, input: Record<string, unknown>, state: Parameters<typeof toolFrame>[1], options: ProviderRowOptions = {}): ToolCall {
  const call = providerToolCall(AgentProvider.MIMO_CODE, toolFrame(tool, { input, ...state }), {
    spanType: tool,
    request: parsedFrame(openingFrame(tool, input)),
    ...options,
  })
  expect(call).not.toBeNull()
  return call!
}

/** The call an opening frame draws while the call still runs. */
function running(tool: string, input: Record<string, unknown>, options: ProviderRowOptions = {}): ToolCall {
  const call = providerToolCall(AgentProvider.MIMO_CODE, openingFrame(tool, input), { spanType: tool, role: 'request', ...options })
  expect(call).not.toBeNull()
  return call!
}

describe('mimoToolCall', () => {
  describe('execute', () => {
    const input = { command: 'echo hi', description: 'Say hi', workdir: '/p' }

    it('states the command, the description and the directory while it runs', () => {
      const call = running(MIMO_TOOL.Bash, input)
      expect(call.kind).toBe('execute')
      expect(call.status).toBe('in_progress')
      expect(call.result).toBeUndefined()
      expect(call.kind === 'execute' && call.request).toEqual({ command: 'echo hi', description: 'Say hi', cwd: '/p' })
      expect(call.title).toBe('Say hi')
    })

    it('reads the output and the exit code from the metadata', () => {
      const call = finished(MIMO_TOOL.Bash, input, {
        output: 'hi\n\n<bash_metadata>note for the model</bash_metadata>',
        metadata: { output: 'hi\n', exit: 0, truncated: false },
      })
      expect(call.status).toBe('completed')
      expect(call.kind === 'execute' && call.result).toEqual({ commands: [{ output: 'hi\n', exitCode: 0 }], unresolvedTerminals: [] })
    })

    it('keeps the printed output of a command that failed', () => {
      const call = finished(MIMO_TOOL.Bash, input, {
        status: MIMO_TOOL_STATUS.Error,
        error: 'Command exited with code 2',
        metadata: { output: 'partial\n', exit: 2 },
      })
      expect(call.status).toBe('failed')
      expect(call.kind === 'execute' && call.result).toEqual({ commands: [{ output: 'partial\n', exitCode: 2 }], unresolvedTerminals: [] })
    })

    it('reads a refused call as declined and an aborted one as cancelled', () => {
      const declined = finished(MIMO_TOOL.Bash, input, {
        status: MIMO_TOOL_STATUS.Error,
        error: 'Error: The user rejected permission to use this specific tool call.',
      })
      expect(declined.status).toBe('declined')
      const aborted = finished(MIMO_TOOL.Bash, input, { status: MIMO_TOOL_STATUS.Error, error: 'Tool execution aborted' })
      expect(aborted.status).toBe('cancelled')
      const interrupted = finished(MIMO_TOOL.Bash, input, { status: MIMO_TOOL_STATUS.Error, error: 'stopped', metadata: { interrupted: true } })
      expect(interrupted.status).toBe('cancelled')
    })

    it('reads a script as JavaScript', () => {
      const call = finished(MIMO_TOOL.Exec, { code: 'return 2' }, { output: '2' })
      expect(call.kind === 'execute' && call.request).toEqual({ command: 'return 2', language: 'javascript' })
    })

    // A script answers in MiMo's `<exec>` wrapper, and the call COMPLETES whether the
    // script did or not: the status in the metadata is the only word on the outcome.
    describe('a script', () => {
      const script = { code: 'return await tools.read({ file_path: "a" })' }

      it('states the value a completed script returned', () => {
        const output = '<exec status="completed">\n<return_value>\n2\n</return_value>\n</exec>'
        const call = finished(MIMO_TOOL.Exec, script, { output, title: '1 tool calls', metadata: { status: 'completed', toolCalls: 1 } })
        expect(call.status).toBe('completed')
        expect(call.kind === 'execute' && call.result).toEqual({ commands: [{ output }], unresolvedTerminals: [] })
      })

      it.each([
        ['code_error', 'failed'],
        ['timeout', 'failed'],
        ['budget_exceeded', 'failed'],
        ['cancelled', 'cancelled'],
      ])('reads a script that ended with %s as %s, with what it printed', (status, expected) => {
        const output = `<exec status="${status}">\n<error_message>\nboom\n</error_message>\n</exec>`
        const call = finished(MIMO_TOOL.Exec, script, { output, title: status, metadata: { status, toolCalls: 0 } })
        expect(call.status).toBe(expected)
        expect(call.kind === 'execute' && call.result).toEqual({ commands: [{ output }], unresolvedTerminals: [] })
      })

      // A script from a release that states no status reads as its call ended.
      it('reads a script that states no status as completed', () => {
        const call = finished(MIMO_TOOL.Exec, script, { output: '2' })
        expect(call.status).toBe('completed')
      })

      // Only a script states its outcome in the metadata. A shell command's
      // metadata carries no such word, and a stray one changes nothing.
      it('reads no script status on a shell command', () => {
        const call = finished(MIMO_TOOL.Bash, input, { output: 'hi\n', metadata: { output: 'hi\n', exit: 0, status: 'code_error' } })
        expect(call.status).toBe('completed')
      })
    })

    // A turn that ends while the call runs closes the span with the call's LAST frame,
    // which still reads as running. LeapMux's own completion column ends the row.
    it('ends a call the turn cut short with the worker completion', () => {
      const call = providerToolCall(AgentProvider.MIMO_CODE, openingFrame(MIMO_TOOL.Bash, input), {
        spanType: MIMO_TOOL.Bash,
        completion: MessageCompletion.INTERRUPTED,
      })
      expect(call?.status).toBe('cancelled')
      expect(call?.result).toBeUndefined()
    })

    // The last update of a command that the turn cut short is the only record of
    // what the command printed before the cut.
    it('keeps the output that a cut command printed before the cut', () => {
      const cut = toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Running, input, metadata: { output: 'partial\n' } })
      const call = providerToolCall(AgentProvider.MIMO_CODE, cut, {
        spanType: MIMO_TOOL.Bash,
        completion: MessageCompletion.INTERRUPTED,
        request: parsedFrame(openingFrame(MIMO_TOOL.Bash, input)),
      })
      expect(call?.status).toBe('cancelled')
      expect(call?.kind === 'execute' && call.result).toEqual({ commands: [{ output: 'partial\n' }], unresolvedTerminals: [] })
    })

    // Only the row that the worker ended states the partial output. The opening row
    // of a call that still runs states none, although a later update is its result.
    it('states no partial output on an opening row', () => {
      const later = parsedFrame(toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Running, input, metadata: { output: 'partial\n' } }))
      const call = running(MIMO_TOOL.Bash, input, { result: later })
      expect(call.status).toBe('in_progress')
      expect(call.result).toBeUndefined()
    })

    // The opening row reads the call's final frame when the span holds it, so its
    // header states the outcome rather than a call still in flight.
    it('reads the final frame on the opening row', () => {
      const result = parsedFrame(toolFrame(MIMO_TOOL.Bash, { input, output: 'hi\n', metadata: { output: 'hi\n', exit: 0 } }))
      const call = running(MIMO_TOOL.Bash, input, { result })
      expect(call.status).toBe('completed')
    })
  })

  describe('read', () => {
    it('reads the numbered file body and the range notice', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p/a.ts', offset: 1, limit: 2 }, {
        output: '<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: alpha\n2: beta\n\n(Showing lines 1-2 of 9. Use offset=3 to continue.)\n</content>',
      })
      expect(call.kind).toBe('read')
      expect(call.kind === 'read' && call.request).toEqual({ path: '/p/a.ts', offset: 1, limit: 2 })
      expect(call.kind === 'read' && call.result).toEqual({
        lines: [{ num: 1, text: 'alpha' }, { num: 2, text: 'beta' }],
        fallbackContent: 'alpha\nbeta',
        trailing: [{ label: 'Range', text: 'Showing lines 1-2 of 9. Use offset=3 to continue.' }],
      })
    })

    it('takes the list kind for a directory', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p' }, {
        output: '<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\nb.ts\n\n(Showing 2 of 5 entries. Use \'offset\' parameter to read beyond entry 3)\n</entries>',
      })
      expect(call.kind).toBe('list')
      expect(call.kind === 'list' && call.result).toEqual({
        entries: [{ path: 'a.ts' }, { path: 'b.ts' }],
        totalEntries: 5,
        truncated: true,
      })
    })

    it('draws the picture a view_image call attached', () => {
      const call = finished(MIMO_TOOL.ViewImage, { path: '/p/dot.png' }, {
        output: 'Image read successfully',
        attachments: [{ type: 'file', mime: 'image/png', url: 'data:image/png;base64,iVBORw0KGgo=' }],
      })
      expect(call.kind).toBe('read')
      expect(call.images).toEqual([{ mimeType: 'image/png', data: 'iVBORw0KGgo=', filePath: '/p/dot.png' }])
    })
  })

  describe('file changes', () => {
    const diff = '--- /p/a.ts\n+++ /p/a.ts\n@@ -1,1 +1,1 @@\n-alpha\n+omega\n'

    it('states the change an edit asked for, then the diff that landed', () => {
      const input = { file_path: '/p/a.ts', old_string: 'alpha', new_string: 'omega' }
      const open = running(MIMO_TOOL.Edit, input)
      expect(open.kind === 'edit' && open.request.changes).toEqual([{ filePath: '/p/a.ts', operation: 'edit', oldStr: 'alpha', newStr: 'omega', structuredPatch: null }])
      const call = finished(MIMO_TOOL.Edit, input, { output: 'Edit applied successfully.', metadata: { diff, filediff: { file: '/p/a.ts', patch: diff } } })
      expect((call.kind === 'edit' ? typedResult(call) : undefined)?.changes[0]?.filePath).toBe('/p/a.ts')
      expect((call.kind === 'edit' ? typedResult(call) : undefined)?.changes[0]?.structuredPatch).toHaveLength(1)
    })

    it('reads a new file as an add, and an overwrite as an edit', () => {
      const added = finished(MIMO_TOOL.Write, { file_path: '/p/new.ts', content: 'x\n' }, {
        output: 'Wrote file successfully.',
        metadata: { diff: '--- /p/new.ts\n+++ /p/new.ts\n@@ -0,0 +1,1 @@\n+x\n', filepath: '/p/new.ts', exists: false },
      })
      expect((added.kind === 'write' ? typedResult(added) : undefined)?.changes[0]?.operation).toBe('add')
      const replaced = finished(MIMO_TOOL.Write, { file_path: '/p/a.ts', content: 'y\n' }, {
        output: 'Wrote file successfully.',
        metadata: { diff: '--- /p/a.ts\n+++ /p/a.ts\n@@ -1,1 +1,1 @@\n-x\n+y\n', filepath: '/p/a.ts', exists: true },
      })
      expect((replaced.kind === 'write' ? typedResult(replaced) : undefined)?.changes[0]?.operation).toBe('edit')
    })

    it('reads each file of a patch', () => {
      const call = finished(MIMO_TOOL.ApplyPatch, { patch_text: '*** Begin Patch\n*** Update File: /p/a.ts\n@@\n-alpha\n+omega\n*** End Patch' }, {
        output: 'Success.',
        metadata: { files: [{ filePath: '/p/a.ts', type: 'update', patch: diff }, { filePath: '/p/b.ts', movePath: '/p/c.ts', type: 'move', patch: '' }] },
      })
      const changes = call.kind === 'edit' ? typedResult(call)?.changes : undefined
      expect(changes?.map(change => [change.filePath, change.operation, change.previousPath])).toEqual([
        ['/p/a.ts', 'edit', undefined],
        ['/p/c.ts', 'move', '/p/b.ts'],
      ])
    })

    it('reads every replacement of a multiedit', () => {
      const call = running(MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: [{ old_string: 'a', new_string: 'b' }, { old_string: 'c', new_string: 'd' }] })
      expect(call.kind === 'edit' && call.request.changes.map(change => change.newStr)).toEqual(['b', 'd'])
    })

    // A multiedit runs MiMo's edit once for each replacement, and states each edit's
    // own metadata under `results`. It states no diff of its own.
    it('reads the diff that each replacement of a multiedit landed', () => {
      const first = '--- /p/a.ts\n+++ /p/a.ts\n@@ -1,1 +1,1 @@\n-a\n+b\n'
      const second = '--- /p/a.ts\n+++ /p/a.ts\n@@ -3,1 +3,1 @@\n-c\n+d\n'
      const call = finished(MIMO_TOOL.MultiEdit, { file_path: '/p/a.ts', edits: [{ old_string: 'a', new_string: 'b' }, { old_string: 'c', new_string: 'd' }] }, {
        output: 'Edit applied successfully.',
        title: 'a.ts',
        metadata: { results: [{ diff: first, filediff: { file: '/p/a.ts', patch: first } }, { diff: second, filediff: { file: '/p/a.ts', patch: second } }] },
      })
      const changes = call.kind === 'edit' ? typedResult(call)?.changes : undefined
      expect(changes?.map(change => [change.filePath, change.structuredPatch?.[0]?.oldStart])).toEqual([['/p/a.ts', 1], ['/p/a.ts', 3]])
    })

    it('reads the diff a notebook edit landed', () => {
      const notebookDiff = '--- /p/n.ipynb\n+++ /p/n.ipynb\n@@ -1,1 +1,1 @@\n-print(1)\n+print(2)\n'
      const call = finished(MIMO_TOOL.NotebookEdit, { notebook_path: '/p/n.ipynb', cell_id: 'c1', new_source: 'print(2)' }, {
        output: 'Notebook updated: replace on n.ipynb.',
        title: 'n.ipynb — replace cell c1',
        metadata: { diff: notebookDiff, edit_mode: 'replace', cell_id: 'c1' },
      })
      const changes = call.kind === 'edit' ? typedResult(call)?.changes : undefined
      expect(changes?.map(change => change.filePath)).toEqual(['/p/n.ipynb'])
      expect(changes?.[0]?.structuredPatch).toHaveLength(1)
    })

    it('takes the generic card for a change that names no file', () => {
      const call = finished(MIMO_TOOL.Edit, { old_string: 'a', new_string: 'b' }, { output: 'Edit applied successfully.' })
      expect(call.kind).toBe('other')
    })

    it('states the requested change when no diff landed', () => {
      const call = finished(MIMO_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'a', new_string: 'b' }, { output: 'Edit applied successfully.' })
      expect((call.kind === 'edit' ? typedResult(call) : undefined)?.changes).toEqual([{ filePath: '/p/a.ts', operation: 'edit', oldStr: 'a', newStr: 'b', structuredPatch: null }])
    })
  })

  describe('searches', () => {
    it('reads a glob listing', () => {
      const call = finished(MIMO_TOOL.Glob, { pattern: '*.ts' }, { output: '/p/a.ts\n/p/b.ts', metadata: { count: 2, truncated: false } })
      expect(call.kind === 'glob' && call.result).toMatchObject({ filenames: ['/p/a.ts', '/p/b.ts'], numFiles: 2, empty: false })
    })

    it('reads a grep listing into structured matches', () => {
      const call = finished(MIMO_TOOL.Grep, { pattern: 'alpha' }, { output: 'Found 1 matches\n/p/a.ts:\n  Line 3: alpha', metadata: { matches: 1, truncated: false } })
      expect(call.kind === 'grep' && call.result).toMatchObject({ lines: [{ filePath: '/p/a.ts', lineNumber: 3, text: 'alpha' }], numFiles: 1, matchCount: 1 })
    })

    it('keeps a body it cannot read as text', () => {
      const call = finished(MIMO_TOOL.Grep, { pattern: 'alpha' }, { output: 'something else', metadata: { matches: 1 } })
      expect(call.result).toEqual({ unparsed: true, text: 'something else' })
    })

    it('reads a grep that skipped paths, and states why beside its matches', () => {
      const call = finished(MIMO_TOOL.Grep, { pattern: 'alpha' }, {
        output: 'Found 1 matches\n/p/a.ts:\n  Line 3: alpha\n\n(Some paths were inaccessible and skipped)',
        metadata: { matches: 1, truncated: false },
      })
      expect(call.kind === 'grep' && call.result).toMatchObject({
        lines: [{ filePath: '/p/a.ts', lineNumber: 3, text: 'alpha' }],
        notice: 'Some paths were inaccessible and skipped',
        truncated: false,
      })
    })
  })

  describe('subagents', () => {
    const spawn = { operation: { action: 'spawn', subagent_type: 'general', description: 'Helper', prompt: 'Work.' } }

    it('states the launch and the registry row the worker keys by the call', () => {
      const call = running(MIMO_TOOL.Actor, spawn)
      expect(call.kind === 'agent' && call.request).toEqual({ description: 'Helper', agentType: 'general', prompt: 'Work.', registryKey: 'call-1' })
      expect(call.title).toBe('Helper')
    })

    it('reads a background spawn as a running subagent', () => {
      const call = finished(MIMO_TOOL.Actor, spawn, {
        output: 'Background sub-session started. actor_id: general-1\nThe result will be delivered as a notification when complete.',
        metadata: { actorId: 'general-1', model: { modelID: 'alpha' } },
      })
      const run = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
      expect(run).toMatchObject({ agentId: 'general-1', outcome: 'running', registryKey: 'call-1', body: 'Work.', bodyLabel: 'Prompt' })
      expect(run?.metadata).toEqual([{ label: 'Agent ID', value: 'general-1' }, { label: 'Agent', value: 'general' }, { label: 'Model', value: 'alpha' }])
    })

    it('reads a blocking run\'s report', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { ...spawn.operation, action: 'run' } }, {
        output: 'actor_id: explore-1 (to give this subagent more work, `send` it another message)\n\n<actor_result status="success" summary="Found it">\nThe answer is 42.\n</actor_result>',
        metadata: { actorId: 'explore-1' },
      })
      const run = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
      expect(run).toMatchObject({ agentId: 'explore-1', outcome: 'completed', statusLabel: 'success', body: 'The answer is 42.' })
    })

    it('reads a message to a subagent, and an operation on one', () => {
      const send = running(MIMO_TOOL.Actor, { operation: { action: 'send', to_actor_id: 'general-1', content: 'Faster.' } })
      expect(send.kind === 'message' && send.request).toEqual({ to: 'general-1', text: 'Faster.' })
      const cancel = running(MIMO_TOOL.Actor, { operation: { action: 'cancel', actor_id: 'general-1' } })
      expect(cancel.kind === 'task' && cancel.request).toEqual({ action: 'stop', taskId: 'general-1' })
      const unread = running(MIMO_TOOL.Actor, { operation: '{"action":"spawn"}' })
      expect(unread.kind).toBe('other')
    })

    // A run whose task_id names no task still runs, and MiMo puts a note in front of
    // the report. The report is still a report, and the note stays beside it.
    it('reads a blocking run\'s report after a task notice', () => {
      const note = 'note: task_id "T9" does not exist in this session; ran ad-hoc. Create it with the `task` tool first, or omit task_id.'
      const call = finished(MIMO_TOOL.Actor, { operation: { ...spawn.operation, action: 'run', task_id: 'T9' } }, {
        output: `${note}\n\nactor_id: explore-1 (to give this subagent more work, \`send\` it another message)\n\n<actor_result status="success" summary="Found">\nThe answer is 42.\n</actor_result>`,
        metadata: { actorId: 'explore-1' },
      })
      const run = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
      expect(run).toMatchObject({ agentId: 'explore-1', outcome: 'completed', statusLabel: 'success', body: 'The answer is 42.' })
      expect(run?.metadata).toContainEqual({ label: 'Note', value: note })
    })

    it('reads a background spawn after a task notice', () => {
      const note = 'note: task_id "x" is not a valid task ID (expected Tn or Tn.m); ran ad-hoc. Task IDs come from the `task` tool.'
      const call = finished(MIMO_TOOL.Actor, spawn, {
        output: `${note}\nBackground sub-session started. actor_id: general-1\nThe result will be delivered as a notification when complete.`,
        metadata: { actorId: 'general-1' },
      })
      const run = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
      expect(run).toMatchObject({ agentId: 'general-1', outcome: 'running' })
      expect(run?.metadata).toContainEqual({ label: 'Note', value: note })
    })

    // MiMo completes an `actor send` to an actor it does not know, and states the
    // failure in its title and its metadata alone.
    it('reads a message that reached no subagent as failed', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { action: 'send', to_actor_id: 'ghost-1', content: 'hi' } }, {
        output: '{"inboxID":null,"error":"receiver not found"}',
        title: 'Send failed: receiver not found',
        metadata: { receiver_actor_id: 'ghost-1', error: 'receiver not found' },
      })
      expect(call.kind).toBe('message')
      expect(call.status).toBe('failed')
      expect(call.result).toEqual({ failure: true, text: 'Send failed: receiver not found' })
    })

    it('reads a delivered message as sent', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { action: 'send', to_actor_id: 'general-1', content: 'hi' } }, {
        output: '{"inboxID":"01M37"}',
        title: 'Sent to general-1',
        metadata: { inboxID: '01M37', receiver_actor_id: 'general-1' },
      })
      expect(call.status).toBe('completed')
      expect(call.result).toEqual({ text: '{"inboxID":"01M37"}', format: 'plain' })
    })

    // `status` states pending, running or idle; `wait` adds `timeout` and the last
    // outcome; `cancel` states `cancelled`; an actor MiMo cannot find is `unknown`.
    describe('an operation on a subagent', () => {
      const operation = (action: string) => ({ operation: { action, actor_id: 'general-1' } })
      const outcomeOf = (action: string, metadata: Record<string, unknown>, output = JSON.stringify(metadata)) => {
        const call = finished(MIMO_TOOL.Actor, operation(action), { output, metadata })
        expect(call.status).toBe('completed')
        return call.kind === 'task' ? typedResult(call)?.outcome : undefined
      }

      it.each([
        ['a wait on a subagent that failed', 'wait', { actor_id: 'general-1', status: 'idle', lastOutcome: 'failure' }, 'failed'],
        ['a wait on a subagent that was cancelled', 'wait', { actor_id: 'general-1', status: 'idle', lastOutcome: 'cancelled' }, 'stopped'],
        ['a wait on a subagent that succeeded', 'wait', { actor_id: 'general-1', status: 'idle', lastOutcome: 'success' }, 'completed'],
        ['a wait that timed out while the subagent runs', 'wait', { actor_id: 'general-1', status: 'timeout' }, 'running'],
        ['the status of a running subagent', 'status', { actor_id: 'general-1', status: 'running' }, 'running'],
        ['the status of a subagent that waits for its first turn', 'status', { actor_id: 'general-1', status: 'pending' }, 'running'],
        ['a cancel', 'cancel', { actor_id: 'general-1', status: 'cancelled' }, 'stopped'],
        ['a cancel of a subagent that already ended', 'cancel', { actor_id: 'general-1', status: 'idle' }, 'completed'],
        ['an operation on a subagent MiMo does not know', 'status', { actor_id: 'ghost-1', status: 'unknown' }, 'failed'],
      ])('reads %s', (_name, action, metadata, outcome) => {
        expect(outcomeOf(action, metadata)).toBe(outcome)
      })

      // The status snapshot states no last outcome. It states the error of the last
      // turn, which MiMo keeps for a failure alone.
      it('reads the status of an idle subagent from the error its snapshot states', () => {
        expect(outcomeOf('status', { actor_id: 'general-1', status: 'idle' }, JSON.stringify({ status: 'idle', actor_id: 'general-1', error: 'model refused' }))).toBe('failed')
        expect(outcomeOf('status', { actor_id: 'general-1', status: 'idle' }, JSON.stringify({ status: 'idle', actor_id: 'general-1' }))).toBe('completed')
        expect(outcomeOf('status', { actor_id: 'general-1', status: 'idle' }, 'not JSON')).toBe('completed')
      })

      it('reads the model list as completed', () => {
        expect(outcomeOf('models', {}, 'mock/alpha\nmock/beta')).toBe('completed')
      })

      // A workflow states its own words, which the actor reading does not share.
      it.each([
        ['failed', 'failed'],
        ['cancelled', 'stopped'],
        ['running', 'running'],
        ['completed', 'completed'],
      ])('reads a workflow run that states %s as %s', (status, outcome) => {
        const call = finished(MIMO_TOOL.Workflow, { operation: 'wait', run_id: 'wf_1' }, { output: status, metadata: { runID: 'wf_1', status } })
        expect(call.kind === 'task' && typedResult(call)?.outcome).toBe(outcome)
      })
    })
  })

  describe('to-dos', () => {
    it('draws the item a create added', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'create', summary: 'Ship it' } }, {
        output: 'Created T1 (open): Ship it',
        title: 'Task created: T1',
        metadata: { id: 'T1', status: 'open' },
      })
      expect(call.title).toBe('Task created: T1')
      expect((call.kind === 'todo' ? typedResult(call) : undefined)?.items).toEqual([{ id: 'T1', rowKey: 'T1', content: 'Ship it', status: 'pending', activeForm: '' }])
    })

    it('draws the status a change left', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'done', id: 'T1', event_summary: 'shipped' } }, {
        output: 'done → done',
        metadata: { id: 'T1', status: 'done' },
      })
      expect(call.kind === 'todo' && call.result).toEqual({ items: [{ id: 'T1', rowKey: 'T1', content: 'T1', status: 'completed', activeForm: '' }], note: 'shipped' })
    })

    it('draws every item a list printed', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'list' } }, { output: 'T1 in_progress — Ship it\nT2 blocked — Wait', metadata: { count: 2 } })
      expect((call.kind === 'todo' ? typedResult(call) : undefined)?.items.map(item => [item.id, item.status])).toEqual([['T1', 'in_progress'], ['T2', 'pending']])
    })

    it('draws the item a get read', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'get', id: 'T1' } }, {
        output: JSON.stringify({ id: 'T1', summary: 'Ship it', status: 'in_progress' }),
        title: 'Task T1',
      })
      expect(call.kind === 'todo' && call.request).toEqual({ items: [] })
      expect((call.kind === 'todo' ? typedResult(call) : undefined)?.items).toEqual([{ id: 'T1', rowKey: 'T1', content: 'Ship it', status: 'in_progress', activeForm: '' }])
    })

    it('draws the new text a rename gave', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'rename', id: 'T1', summary: 'Ship it today' } }, {
        output: 'Renamed T1',
        metadata: { id: 'T1', status: 'open' },
      })
      expect((call.kind === 'todo' ? typedResult(call) : undefined)?.items).toEqual([{ id: 'T1', rowKey: 'T1', content: 'Ship it today', status: 'pending', activeForm: '' }])
    })

    it('draws an abandoned item as deleted, with its note', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'abandon', id: 'T2', event_summary: 'no longer needed' } }, {
        output: 'open → abandoned',
        metadata: { id: 'T2', status: 'abandoned' },
      })
      expect(call.kind === 'todo' && call.result).toEqual({ items: [{ id: 'T2', rowKey: 'T2', content: 'T2', status: 'deleted', activeForm: '' }], note: 'no longer needed' })
    })

    it('keeps an answer it cannot read as text', () => {
      const call = finished(MIMO_TOOL.Task, { operation: { action: 'get', id: 'T1' } }, { output: 'No task T1.' })
      expect(call.result).toEqual({ unparsed: true, text: 'No task T1.' })
    })
  })

  describe('plan approval', () => {
    it('reads an approval as the switch to build', () => {
      const call = finished(MIMO_TOOL.PlanExit, {}, {
        output: 'User approved switching to build agent.',
        title: 'Switching to build agent',
        metadata: { switched: true, feedback: '' },
      })
      expect(call.kind).toBe('switch_mode')
      expect(call.status).toBe('completed')
      expect(call.kind === 'switch_mode' && call.request.mode).toBe('build')
    })

    // A plan sent back is an answer, not a failure: the header states it with the
    // words the request carries for that case, and the body states the feedback.
    it('reads a plan the reader sent back as declined, with the feedback', () => {
      const call = finished(MIMO_TOOL.PlanExit, {}, {
        output: 'User chose not to switch yet and provided feedback: Add tests.',
        metadata: { switched: false, feedback: 'Add tests.' },
      })
      expect(call.status).toBe('declined')
      expect(call.result).toEqual({ text: 'Add tests.', format: 'plain' })
    })

    it('reads a plan the reader rejected with no words as declined, with MiMo\'s answer', () => {
      const output = 'User chose to stay in plan mode and continue refining the plan.'
      const call = finished(MIMO_TOOL.PlanExit, {}, { output, title: 'Staying in plan mode', metadata: { switched: false, feedback: '' } })
      expect(call.status).toBe('declined')
      expect(call.kind === 'switch_mode' && call.request).toEqual({ mode: 'build', declinedTitle: 'Plan sent back' })
      expect(call.result).toEqual({ text: output, format: 'plain' })
    })

    // The build agent can call the plan tool too. MiMo then asks nobody and answers
    // that plan mode is not active: nothing was approved and nothing was sent back.
    it('reads a call outside plan mode as a plain completed call', () => {
      const output = 'You are not in plan mode. This tool is only effective in plan mode.'
      const call = finished(MIMO_TOOL.PlanExit, {}, { output, title: 'Not in plan mode', metadata: { switched: false, feedback: '' } })
      expect(call.kind).toBe('switch_mode')
      expect(call.status).toBe('completed')
      expect(call.title).toBe('Not in plan mode')
      expect(call.kind === 'switch_mode' && call.request).toEqual({})
      expect(call.result).toEqual({ text: output, format: 'plain' })
    })
  })

  describe('questions', () => {
    it('reads each answer beside its question', () => {
      const call = finished(MIMO_TOOL.Question, {
        questions: [{ question: 'Which database?', header: 'Database', options: [{ label: 'SQLite' }] }, { question: 'Why?', options: [] }],
      }, { output: 'User has answered your questions.', metadata: { answers: [['SQLite'], []] } })
      expect(call.kind === 'question' && call.result).toEqual({ answers: [{ header: 'Database', answer: 'SQLite' }, { header: 'Why?', answer: null }] })
    })
  })

  describe('scheduled jobs and workflows', () => {
    it('reads a cron schedule as a trigger it creates', () => {
      const call = running(MIMO_TOOL.Cron, { operation: { action: 'schedule', cron: '0 9 * * 1', prompt: 'Report' } })
      expect(call.kind === 'trigger' && call.request).toEqual({ action: 'create', name: 'Report', schedule: '0 9 * * 1' })
    })

    it('reads a workflow status call as a task read', () => {
      const call = finished(MIMO_TOOL.Workflow, { operation: 'status', run_id: 'wf_1' }, { output: 'running', metadata: { runID: 'wf_1', status: 'running' } })
      expect(call.kind === 'task' && call.request).toEqual({ action: 'output', taskId: 'wf_1' })
      expect(call.kind === 'task' && call.result).toEqual({ outcome: 'running', output: 'running' })
    })
  })

  it('draws the generic card for a tool from a later release', () => {
    const call = finished('telepathy', { target: 'x' }, { output: 'done' })
    expect(call.kind).toBe('other')
    expect(call.result).toEqual({ content: [{ type: 'text', text: 'done' }] })
  })

  // A Model Context Protocol tool answers with text and file attachments. Each image
  // follows the text on the generic card, and a file of another type states nothing.
  it('draws the images that a Model Context Protocol tool attached', () => {
    const call = finished('docs_lookup', { query: 'q' }, {
      output: 'Found it',
      attachments: [
        { type: 'file', mime: 'image/png', url: 'data:image/png;base64,iVBORw0KGgo=' },
        { type: 'file', mime: 'application/pdf', url: 'data:application/pdf;base64,JVBERi0=' },
      ],
      metadata: { mcp: { isError: false } },
    })
    expect(call.kind).toBe('other')
    expect(call.result).toEqual({
      content: [
        { type: 'text', text: 'Found it' },
        { type: 'image', source: { mimeType: 'image/png', data: 'iVBORw0KGgo=' } },
      ],
    })
  })

  it('states no text item for a tool that printed nothing', () => {
    const call = finished('docs_lookup', { query: 'q' }, { output: '' })
    expect(call.result).toEqual({ content: [] })
  })

  it('draws no row for a pending frame', () => {
    const row = providerRow(AgentProvider.MIMO_CODE, toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Pending }))
    expect(row).toEqual({ kind: 'hidden' })
  })

  // The icon tooltip states the tool's own name, whatever kind the call takes.
  it('labels the call with the tool\'s own name', () => {
    expect(finished(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, { output: 'x' }).label).toBe(MIMO_TOOL.Read)
    expect(finished('docs_lookup', { query: 'q' }, { output: 'x' }).label).toBe('docs_lookup')
  })

  // A stored row always states its tool. A frame that lost the name still draws the
  // generic card with what it printed, and claims no label.
  it('draws the generic card for a call that states no tool name', () => {
    const call = finished('', { target: 'x' }, { output: 'done' })
    expect(call.kind).toBe('unspecified')
    expect(call.label).toBeUndefined()
    expect(call.result).toEqual({ content: [{ type: 'text', text: 'done' }] })
  })

  // MiMo can end a call in an error that states no words. The row still states why
  // it has no answer.
  it('states a failure that gives no words as a failed call', () => {
    const call = finished(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, { status: MIMO_TOOL_STATUS.Error, error: '' })
    expect(call.status).toBe('failed')
    expect(call.result).toEqual({ failure: true, text: 'Tool call failed' })
  })

  describe('reads', () => {
    // A read of a picture answers with a sentence and the picture as an attachment.
    // The attachment names its file by basename alone, so the call's own path heads
    // the picture.
    it('draws the picture a read of an image file attached, on the path the call read', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p/dot.png' }, {
        output: 'Image read successfully',
        attachments: [{ type: 'file', mime: 'image/png', filename: 'dot.png', url: 'data:image/png;base64,iVBORw0KGgo=' }],
      })
      expect(call.kind).toBe('read')
      expect(call.images).toEqual([{ mimeType: 'image/png', data: 'iVBORw0KGgo=', filePath: '/p/dot.png' }])
      expect(call.result).toEqual({ unparsed: true, text: 'Image read successfully' })
    })

    it('states the reminder MiMo appended after a file with no range notice', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, {
        output: '<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: alpha\n</content>\n<system-reminder>\nFollow AGENTS.md.\n</system-reminder>',
      })
      expect(call.kind === 'read' && call.result).toEqual({
        lines: [{ num: 1, text: 'alpha' }],
        fallbackContent: 'alpha',
        trailing: [{ label: 'System Reminder', text: 'Follow AGENTS.md.' }],
      })
    })

    it('puts the range notice ahead of the reminders', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, {
        output: '<path>/p/a.ts</path>\n<type>file</type>\n<content>\n1: alpha\n\n(End of file - total 1 lines)\n</content>\n<system-reminder>\nFollow AGENTS.md.\n</system-reminder>',
      })
      expect(call.kind === 'read' ? typedResult(call)?.trailing : undefined).toEqual([
        { label: 'Range', text: 'End of file - total 1 lines' },
        { label: 'System Reminder', text: 'Follow AGENTS.md.' },
      ])
    })

    // A read of a directory can skip entries. The result states where the page began.
    it('states the offset of a directory page that skipped entries', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p', offset: 5 }, {
        output: '<path>/p</path>\n<type>directory</type>\n<entries>\ne.ts\n\n(Showing 1 of 9 entries. Use \'offset\' parameter to read beyond entry 6)\n</entries>',
      })
      expect(call.kind).toBe('list')
      expect(call.kind === 'list' && call.request).toEqual({ path: '/p' })
      expect(call.kind === 'list' && call.result).toEqual({ entries: [{ path: 'e.ts' }], totalEntries: 9, offset: 5, truncated: true })
    })

    it('reads a whole directory as a list that is not truncated', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p' }, { output: '<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\n\n(1 entries)\n</entries>' })
      expect(call.kind === 'list' && call.result).toEqual({ entries: [{ path: 'a.ts' }], truncated: false })
    })

    // Only the answer says whether a read hit a directory, so a read that has not
    // answered, or that failed, stays a read.
    it('keeps a read that has not answered or that failed on the read kind', () => {
      expect(running(MIMO_TOOL.Read, { file_path: '/p' }).kind).toBe('read')
      expect(finished(MIMO_TOOL.Read, { file_path: '/p' }, { status: MIMO_TOOL_STATUS.Error, error: 'EISDIR' }).kind).toBe('read')
    })

    it('keeps a body it cannot parse as text', () => {
      const call = finished(MIMO_TOOL.Read, { file_path: '/p/a.ts' }, { output: 'The file is binary.' })
      expect(call.result).toEqual({ unparsed: true, text: 'The file is binary.' })
    })
  })

  describe('edits and writes', () => {
    it('states a replace-all edit', () => {
      const call = running(MIMO_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'a', new_string: 'b', replace_all: true })
      expect(call.kind === 'edit' && call.request.replaceAll).toBe(true)
      // Only a true flag replaces every match.
      const once = running(MIMO_TOOL.Edit, { file_path: '/p/a.ts', old_string: 'a', new_string: 'b', replace_all: 'yes' })
      expect(once.kind).toBe('edit')
      expect(once.kind === 'edit' ? once.request : null).not.toHaveProperty('replaceAll')
    })

    it('states the requested body of a write that landed no diff', () => {
      const call = finished(MIMO_TOOL.Write, { file_path: '/p/new.ts', content: 'x\n' }, { output: 'Wrote file successfully.' })
      expect(call.kind === 'write' ? typedResult(call)?.changes : undefined).toEqual([{ filePath: '/p/new.ts', operation: 'add', oldStr: '', newStr: 'x\n', structuredPatch: null }])
    })

    it('takes the generic card for a write that names no file', () => {
      expect(finished(MIMO_TOOL.Write, { content: 'x' }, { output: 'Wrote file successfully.' }).kind).toBe('other')
    })
  })

  describe('glob and grep answers', () => {
    it('states the notice of a glob that stopped at its limit', () => {
      const notice = 'Results are truncated: showing first 1 results. Consider using a more specific path or pattern.'
      const call = finished(MIMO_TOOL.Glob, { pattern: '*.ts' }, { output: `/p/a.ts\n\n(${notice})`, metadata: { count: 1, truncated: true } })
      expect(call.kind === 'glob' && call.result).toMatchObject({ filenames: ['/p/a.ts'], numFiles: 1, truncated: true, notice, empty: false })
    })

    it('reads a glob that found nothing as empty', () => {
      const call = finished(MIMO_TOOL.Glob, { pattern: '*.rs' }, { output: 'No files found', metadata: { count: 0, truncated: false } })
      expect(call.kind === 'glob' && call.result).toMatchObject({ filenames: [], numFiles: 0, empty: true })
    })

    // The count is what proves the listing whole. A glob that states none is text.
    it('keeps a glob that states no count as text', () => {
      const call = finished(MIMO_TOOL.Glob, { pattern: '*.ts' }, { output: '/p/a.ts' })
      expect(call.result).toEqual({ unparsed: true, text: '/p/a.ts' })
    })

    it('reads a grep that found nothing as empty, with its count', () => {
      const call = finished(MIMO_TOOL.Grep, { pattern: 'zeta' }, { output: 'No files found', metadata: { matches: 0, truncated: false } })
      expect(call.kind === 'grep' && call.result).toMatchObject({ lines: [], numFiles: 0, numLines: 0, matchCount: 0, empty: true })
    })

    it('counts each file of a grep once', () => {
      const call = finished(MIMO_TOOL.Grep, { pattern: 'a' }, { output: 'Found 3 matches\n/p/a.ts:\n  Line 1: a\n  Line 2: a\n\n/p/b.ts:\n  Line 1: a', metadata: { matches: 3 } })
      expect(call.kind === 'grep' && call.result).toMatchObject({ numFiles: 2, numLines: 3, matchCount: 3 })
    })
  })

  describe('corpus searches', () => {
    it('states the words a code search printed as its matches', () => {
      const call = finished(MIMO_TOOL.CodeSearch, { query: 'react hooks' }, { output: 'const [a, setA] = useState(0)', metadata: { truncated: true } })
      expect(call.kind === 'search' && call.result).toEqual({
        filenames: [],
        content: 'const [a, setA] = useState(0)',
        numFiles: 0,
        numLines: 0,
        truncated: true,
        fallbackContent: 'const [a, setA] = useState(0)',
        empty: false,
      })
    })

    it.each([
      ['printed nothing', ''],
      ['printed only whitespace', '  \n'],
      ['found nothing', 'No files found'],
    ])('reads a search that %s as empty', (_name, output) => {
      const call = finished(MIMO_TOOL.SkillSearch, { query: 'deploy' }, { output })
      expect(call.kind === 'search' && call.result).toMatchObject({ empty: true })
    })
  })

  describe('prose answers', () => {
    it('states a fetch\'s body and a web search\'s summary', () => {
      const fetch = finished(MIMO_TOOL.WebFetch, { url: 'https://example.com' }, { output: '# Example Domain' })
      expect(fetch.kind === 'fetch' && fetch.request).toEqual({ url: 'https://example.com' })
      expect(fetch.result).toEqual({ result: '# Example Domain' })
      const search = finished(MIMO_TOOL.WebSearch, { query: 'mimo code' }, { output: 'Title: MiMo' })
      expect(search.kind === 'web_search' && search.request).toEqual({ query: 'mimo code' })
      expect(search.result).toEqual({ links: [], summary: 'Title: MiMo' })
    })

    it('states a skill as Markdown and a memory as plain text', () => {
      expect(finished(MIMO_TOOL.Skill, { name: 'deploy' }, { output: '# Deploy' }).result).toEqual({ text: '# Deploy', format: 'markdown' })
      expect(finished(MIMO_TOOL.Memory, { operation: 'search', query: 'deploy' }, { output: 'No memories found.' }).result).toEqual({ text: 'No memories found.', format: 'plain' })
    })

    it('states MiMo\'s own title on a scheduled job and on a session call', () => {
      const cron = finished(MIMO_TOOL.Cron, { operation: { action: 'list' } }, { output: 'j1 0 9 * * 1', title: 'Jobs' })
      expect(cron.title).toBe('Jobs')
      expect(cron.result).toEqual({ text: 'j1 0 9 * * 1', format: 'plain' })
      const session = finished(MIMO_TOOL.Session, { action: 'ask', session_id: 'ses_other', question: 'Status?' }, { output: 'All green.', title: 'Asked ses_other' })
      expect(session.kind).toBe('agents')
      expect(session.title).toBe('Asked ses_other')
      expect(session.result).toEqual({ text: 'All green.', format: 'plain' })
    })
  })

  describe('scheduled job requests', () => {
    // `snooze` stands for an action that a later MiMo release can add.
    it.each([
      ['schedule', 'create'],
      ['loop', 'create'],
      ['list', 'list'],
      ['get', 'get'],
      ['delete', 'delete'],
      ['rename', 'update'],
      ['snooze', 'other'],
    ])('reads the %s action as %s', (action, expected) => {
      const call = running(MIMO_TOOL.Cron, { operation: { action } })
      expect(call.kind === 'trigger' && call.request.action).toBe(expected)
    })

    it('states the job a call acts on', () => {
      const call = running(MIMO_TOOL.Cron, { operation: { action: 'delete', id: 'j1' } })
      expect(call.kind === 'trigger' && call.request).toEqual({ action: 'delete', triggerId: 'j1' })
    })

    it('reads an operation that is not an object as no action', () => {
      const call = running(MIMO_TOOL.Cron, { operation: 'list' })
      expect(call.kind === 'trigger' && call.request).toEqual({ action: 'other' })
    })
  })

  describe('operations on subagents and workflow runs', () => {
    it.each([
      ['status', 'output'],
      ['wait', 'output'],
      ['cancel', 'stop'],
    ])('reads the actor %s as the %s action on its subagent', (action, expected) => {
      const call = running(MIMO_TOOL.Actor, { operation: { action, actor_id: 'general-1' } })
      expect(call.kind === 'task' && call.request).toEqual({ action: expected, taskId: 'general-1' })
    })

    it('reads the model list as a list action with no subagent', () => {
      const call = running(MIMO_TOOL.Actor, { operation: { action: 'models' } })
      expect(call.kind === 'task' && call.request).toEqual({ action: 'list' })
    })

    it('reads the subagent from to_actor_id when the operation states no actor_id', () => {
      const call = running(MIMO_TOOL.Actor, { operation: { action: 'wait', to_actor_id: 'general-2' } })
      expect(call.kind === 'task' && call.request).toEqual({ action: 'output', taskId: 'general-2' })
    })

    it('takes the generic card for an actor action this build does not know', () => {
      expect(running(MIMO_TOOL.Actor, { operation: { action: 'teleport', actor_id: 'general-1' } }).kind).toBe('other')
    })

    it.each([
      ['status', 'output'],
      ['wait', 'output'],
      ['cancel', 'stop'],
      ['run', 'other'],
      ['resume', 'other'],
    ])('reads the workflow %s as the %s action on its run', (operation, expected) => {
      const call = running(MIMO_TOOL.Workflow, { operation, run_id: 'wf_1' })
      expect(call.kind === 'task' && call.request).toEqual({ action: expected, taskId: 'wf_1' })
    })

    it('states MiMo\'s own title for the operation', () => {
      const call = finished(MIMO_TOOL.Workflow, { operation: 'status', run_id: 'wf_1' }, { output: 'running', title: 'Workflow wf_1', metadata: { status: 'running' } })
      expect(call.title).toBe('Workflow wf_1')
    })

    // A workflow word of this build's vocabulary that the actor reading does not share.
    it.each([
      ['failure', 'failed'],
      ['pending', 'running'],
      ['a word of a later release', 'completed'],
    ])('reads a workflow run that states %s as %s', (status, outcome) => {
      const call = finished(MIMO_TOOL.Workflow, { operation: 'wait', run_id: 'wf_1' }, { output: status, metadata: { status } })
      expect(call.kind === 'task' && typedResult(call)?.outcome).toBe(outcome)
    })

    // A send that failed on the wire, not one that reached no subagent: the error
    // is the answer, and no status override applies.
    it('reads a send that ended in an error as failed, with the error', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { action: 'send', to_actor_id: 'general-1', content: 'hi' } }, { status: MIMO_TOOL_STATUS.Error, error: 'inbox closed' })
      expect(call.kind).toBe('message')
      expect(call.status).toBe('failed')
      expect(call.result).toEqual({ failure: true, text: 'inbox closed' })
    })

    it('states the metadata error of a send whose call states no title', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { action: 'send', to_actor_id: 'ghost-1', content: 'hi' } }, { output: '{}', metadata: { error: 'receiver not found' } })
      expect(call.status).toBe('failed')
      expect(call.result).toEqual({ failure: true, text: 'receiver not found' })
    })

    // A spawn's answer that matches neither form is still the words MiMo wrote.
    it('keeps a spawn answer it cannot read as text', () => {
      const call = finished(MIMO_TOOL.Actor, { operation: { action: 'spawn', description: 'Helper', prompt: 'Work.' } }, { output: 'The subagent is busy.' })
      expect(call.kind).toBe('agent')
      expect(call.result).toEqual({ unparsed: true, text: 'The subagent is busy.' })
    })
  })

  describe('to-do rows', () => {
    // The renderer composes the header of a call that has not answered.
    it('states no title while the call runs', () => {
      const call = running(MIMO_TOOL.Task, { operation: { action: 'create', summary: 'Ship it' } })
      expect(call.title).toBeUndefined()
      expect(call.kind === 'todo' && call.request.items.map(item => item.content)).toEqual(['Ship it'])
    })

    it('takes the generic card for an operation that is not an object', () => {
      expect(finished(MIMO_TOOL.Task, { operation: 'create' }, { output: 'Invalid operation.' }).kind).toBe('other')
    })
  })

  describe('question rows', () => {
    const questions = [{ question: 'Which database?', header: 'Database', options: [{ label: 'SQLite', description: 'A file.' }, { label: '' }] }]

    it('heads a running question with its header, else its question', () => {
      const call = running(MIMO_TOOL.Question, { questions })
      expect(call.title).toBe('Database')
      expect(call.kind === 'question' && call.request.questions[0]?.options).toEqual([{ label: 'SQLite', description: 'A file.' }])
      expect(running(MIMO_TOOL.Question, { questions: [{ question: 'Which database?' }] }).title).toBe('Which database?')
    })

    it('keeps an answer that states no answers list as text', () => {
      const call = finished(MIMO_TOOL.Question, { questions }, { output: 'User has answered your questions.' })
      expect(call.result).toEqual({ unparsed: true, text: 'User has answered your questions.' })
    })

    it('joins several choices, and states no answer for a choice that is not text', () => {
      const call = finished(MIMO_TOOL.Question, { questions: [...questions, { question: 'Why?' }] }, {
        output: 'User has answered your questions.',
        metadata: { answers: [['SQLite', 'Postgres'], [7, '']] },
      })
      expect(call.kind === 'question' && call.result).toEqual({ answers: [{ header: 'Database', answer: 'SQLite, Postgres' }, { header: 'Why?', answer: null }] })
    })
  })

  describe('plan approval failures', () => {
    // Only a refusal reads as declined. A plan tool that failed or that an abort cut
    // short states its own outcome.
    it('reads a plan tool that failed as failed, and one an abort cut short as cancelled', () => {
      const failed = finished(MIMO_TOOL.PlanExit, {}, { status: MIMO_TOOL_STATUS.Error, error: 'The plan file is missing.' })
      expect(failed.status).toBe('failed')
      expect(failed.result).toEqual({ failure: true, text: 'The plan file is missing.' })
      const aborted = finished(MIMO_TOOL.PlanExit, {}, { status: MIMO_TOOL_STATUS.Error, error: 'Tool execution aborted' })
      expect(aborted.status).toBe('cancelled')
    })

    it('reads a dismissed approval as declined', () => {
      const call = finished(MIMO_TOOL.PlanExit, {}, { status: MIMO_TOOL_STATUS.Error, error: 'The user dismissed this question' })
      expect(call.status).toBe('declined')
      expect(call.result).toEqual({ failure: true, text: 'The user dismissed this question' })
    })

    // The title outside plan mode counts only once the call answered, so a running
    // plan tool still states the switch it asks for.
    it('states the switch to build on a plan tool that has not answered', () => {
      const call = running(MIMO_TOOL.PlanExit, {})
      expect(call.kind === 'switch_mode' && call.request).toEqual({ mode: 'build', declinedTitle: 'Plan sent back' })
    })
  })
})
