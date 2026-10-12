import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCodebuddyMessage } from './classification'

describe('classifyCodebuddyMessage', () => {
  it('classifies a stored Workflow child function call and its output as tool rows', () => {
    const request = { type: 'function_call', id: 'request-record', callId: 'child-read-1', name: 'Read', arguments: '{"file_path":"/work/marker.txt"}' }
    const result = { type: 'function_call_result', id: 'result-record', callId: 'child-read-1', status: 'completed', output: { type: 'text', text: 'CHILD_FILE_MARKER' } }
    const resultAlias = { type: 'function_call_output', call_id: 'child-read-1', output: 'CHILD_FILE_MARKER' }
    expect(classifyCodebuddyMessage(input(request, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_use' })
    expect(classifyCodebuddyMessage(input(result, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_result' })
    expect(classifyCodebuddyMessage(input(resultAlias, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'tool_result' })
  })

  it('hides a stored native progress result until the tool finishes', () => {
    const progress = { type: 'function_call_result', callId: 'child-read-1', status: 'in_progress', output: { type: 'text', text: 'partial' } }
    expect(classifyCodebuddyMessage(input(progress, undefined, AgentProvider.CODEBUDDY))).toEqual({ kind: 'hidden' })
  })

  it('classifies a stored Workflow child answer as assistant text', () => {
    const stored = { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'ARCHIVED_CHILD_TEXT' }] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'assistant_text' })
  })

  it('keeps a stored user record outside assistant text', () => {
    const stored = { type: 'message', role: 'user', content: [{ type: 'input_text', text: 'CHILD_PROMPT' }] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'unknown' })
  })

  it('hides a stored assistant record without output text', () => {
    const stored = { type: 'message', role: 'assistant', content: [] }
    expect(classifyCodebuddyMessage(input(stored, undefined, AgentProvider.CODEBUDDY)))
      .toEqual({ kind: 'hidden' })
  })

  // The worker forwards every `system` line whole (`handleSystem`). These are the shapes that CodeBuddy Code 2.160.0
  // writes in an ordinary turn.
  describe('system', () => {
    const classify = (frame: Record<string, unknown>) => classifyCodebuddyMessage(input(frame, undefined, AgentProvider.CODEBUDDY))

    it.each([
      ['the session start', { type: 'system', subtype: 'init', session_id: 's', model: 'm', permissionMode: 'default', tools: [], mcp_servers: [], slash_commands: [] }],
      ['the heartbeat while the model reasons', { type: 'system', subtype: 'keepalive', session_id: 's', reason: 'reasoning_in_progress', elapsed_ms: 1200 }],
      ['the end of a progress state', { type: 'system', subtype: 'status', status: null, session_id: 's' }],
      ['the start of a compaction that no boundary ends', { type: 'system', subtype: 'status', status: 'compacting', session_id: 's' }],
      ['a background task that the worker records', { type: 'system', subtype: 'task_started', task_id: 't', task_type: 'local_agent' }],
      ['the end of a background task that the worker records', { type: 'system', subtype: 'task_notification', task_id: 't', status: 'completed' }],
      ['the start of the MCP servers', { type: 'system', subtype: 'mcp_status', event: 'start', servers: ['probe'], total_count: 1 }],
      ['the progress of one MCP server', { type: 'system', subtype: 'mcp_status', event: 'server', name: 'probe', state: 'connected', completed: 1, total: 1 }],
      ['an MCP startup in which every server started', { type: 'system', subtype: 'mcp_status', event: 'finish', failed: [], timed_out: [] }],
      ['an informational line with no text', { type: 'system', subtype: 'informational', level: 'warning', content: '  ' }],
    ])('hides %s', (_label, frame) => {
      expect(classify(frame)).toEqual({ kind: 'hidden' })
    })

    it('states each MCP server that failed to start or did not start in time', () => {
      expect(classify({ type: 'system', subtype: 'mcp_status', event: 'finish', failed: ['probe', 'docs'], timed_out: ['slow'] })).toEqual({
        kind: 'notification',
        entries: [
          { kind: 'text', text: 'Failed to start MCP servers: probe, docs' },
          { kind: 'text', text: 'MCP server did not start in time: slow' },
        ],
      })
    })

    it('states an informational line with its level', () => {
      expect(classify({ type: 'system', subtype: 'informational', level: 'warning', content: 'A hook blocked the reply.' })).toEqual({
        kind: 'notification',
        entries: [{ kind: 'text', text: 'Warning: A hook blocked the reply.' }],
      })
      expect(classify({ type: 'system', subtype: 'informational', content: 'The session resumed.' })).toEqual({
        kind: 'notification',
        entries: [{ kind: 'text', text: 'The session resumed.' }],
      })
    })

    it('keeps an unknown subtype as an unrecognized row, so the reader can still inspect it', () => {
      expect(classify({ type: 'system', subtype: 'hologram' })).toEqual({ kind: 'unknown' })
    })
  })

  // The worker forwards these kinds whole from its default branch. These are the shapes that CodeBuddy Code 2.160.0
  // writes.
  describe('control_notification and error', () => {
    const classify = (frame: Record<string, unknown>) => classifyCodebuddyMessage(input(frame, undefined, AgentProvider.CODEBUDDY))

    it.each([
      ['a resolved permission request', { type: 'control_notification', channel: 'permission', data: { toolCallId: 'c', requestId: 'r', decision: 'allow', resolvedBy: 'user' } }],
      ['a completed elicitation', { type: 'control_notification', channel: 'elicitation', data: { id: 'e' } }],
      ['the slash-command list', { type: 'control_notification', channel: 'commands', data: { commands: [], timestamp: 1 } }],
    ])('hides %s', (_label, frame) => {
      expect(classify(frame)).toEqual({ kind: 'hidden' })
    })

    it('states the message of an error line', () => {
      expect(classify({ type: 'error', error: 'Request failed with status code 429' })).toEqual({
        kind: 'notification',
        entries: [{ kind: 'text', text: 'Error: Request failed with status code 429' }],
      })
    })

    it('states an error line with no message as an error', () => {
      expect(classify({ type: 'error', error: '  ' })).toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Error' }] })
      expect(classify({ type: 'error' })).toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Error' }] })
    })
  })
})
