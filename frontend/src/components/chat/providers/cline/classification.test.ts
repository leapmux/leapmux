import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyClineMessage } from './classification'
import { clineToolFinishRow, clineToolStartRow } from './toolResults.fixtures'
import '~/components/chat/providers'

function classify(parent: Record<string, unknown> | undefined, extra: Record<string, unknown> = {}, wrapper?: { old_seqs: number[], messages: unknown[] }) {
  return classifyClineMessage({
    ...input(parent, wrapper, AgentProvider.CLINE),
    ...extra,
  })
}

/** One Cline event envelope. */
function event(name: string, payload: Record<string, unknown> = {}) {
  return {
    version: 'v1',
    event: name,
    sessionId: 's1',
    payload,
  }
}

describe('classifyClineMessage', () => {
  it('reads LeapMux\'s own user row', () => {
    expect(classify({ content: 'hello' })).toEqual({ kind: 'user_content' })
    expect(classify({ content: 'x', hidden: true })).toEqual({ kind: 'user_content' })
    expect(classify({ content: 'x', planExecution: true })).toEqual({ kind: 'plan_execution' })
  })

  it('reads the text, the media and the reasoning of a message', () => {
    expect(classify(event('assistant.finished', { text: 'Hi.' }))).toEqual({ kind: 'assistant_text' })
    expect(classify(event('assistant.media', {
      media: { type: 'image' },
    }))).toEqual({ kind: 'assistant_text' })
    expect(classify(event('reasoning.finished', { reasoning: 'Think.' }))).toEqual({ kind: 'assistant_thinking' })
  })

  it('reads the tool rows by their side of the span', () => {
    expect(classify(clineToolStartRow('read_files', { files: [] }))).toEqual({ kind: 'tool_use' })
    expect(classify(clineToolFinishRow('read_files', []))).toEqual({ kind: 'tool_result' })
  })

  it('reads a retained start row as the result of a turn that stopped', () => {
    for (const completion of [MessageCompletion.INTERRUPTED, MessageCompletion.ERROR, MessageCompletion.COMPLETE]) {
      expect(classify(clineToolStartRow('run_commands', {
        commands: ['sleep 40'],
      }), { completion }), String(completion)).toEqual({ kind: 'tool_result' })
    }
  })

  it('reads a tool row with no call id as a frame it cannot read', () => {
    expect(classify(event('tool.started', { toolName: 'read_files' }))).toEqual({ kind: 'unknown' })
    expect(classify(event('tool.finished', { toolName: 'read_files' }))).toEqual({ kind: 'unknown' })
  })

  it('reads each run end as the turn divider', () => {
    for (const name of ['run.completed', 'run.failed', 'run.aborted'])
      expect(classify(event(name, { reason: 'completed' })), name).toEqual({ kind: 'result_divider' })
  })

  it('leaves stored compaction starts unknown and keeps current notices visible', () => {
    expect(classify(event('session.notice', {
      message: 'auto-compacting',
      metadata: { kind: 'auto_compaction', phase: 'started' },
    })).kind).toBe('unknown')
    expect(classify(event('session.notice', {}))).toEqual({ kind: 'notification', entries: [] })
    expect(classify(event('team.progress', {
      lastEvent: { eventType: 'run_started', runId: 'r1', agentId: 'researcher' },
    })).kind).toBe('notification')
  })

  it('reads a thread of notices', () => {
    const thread = {
      old_seqs: [1, 2],
      messages: [
        event('session.notice', {
          message: 'Context compacted.',
          metadata: { kind: 'auto_compaction', phase: 'completed' },
        }),
      ],
    }
    expect(classify(undefined, {}, thread).kind).toBe('notification')
    expect(classify(undefined, {}, { old_seqs: [], messages: [] })).toEqual({ kind: 'hidden' })
  })

  it('keeps a thread of unclaimed Cline notices visible', () => {
    const thread = {
      old_seqs: [1, 2],
      messages: [
        event('session.notice', {}),
        event('team.progress', {
          lastEvent: { eventType: 'agent_event' },
        }),
      ],
    }
    expect(classify(undefined, {}, thread)).toEqual({ kind: 'notification', entries: [] })
  })

  it('reads LeapMux\'s own notifications, as a thread and as a row', () => {
    expect(classify(undefined, {}, {
      old_seqs: [1],
      messages: [
        { type: 'context_cleared' },
      ],
    }).kind).toBe('notification')
    expect(classify({ type: 'agent_error', error: 'The Cline hub exited.' }).kind).toBe('notification')
  })

  it('reads an event of a later Cline as a frame to inspect', () => {
    expect(classify(event('session.something_new'))).toEqual({ kind: 'unknown' })
    expect(classify({ other: 1 })).toEqual({ kind: 'unknown' })
    expect(classify(undefined)).toEqual({ kind: 'unknown' })
  })
})
