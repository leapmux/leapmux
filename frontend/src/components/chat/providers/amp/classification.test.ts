import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyAmpMessage } from './classification'
import { ampToolResultRow, ampToolUseRow } from './toolResults.fixtures'
import '~/components/chat/providers'

function classify(parent: Record<string, unknown> | undefined, extra: Record<string, unknown> = {}, wrapper?: { old_seqs: number[], messages: unknown[] }) {
  return classifyAmpMessage({ ...input(parent, wrapper, AgentProvider.AMP), ...extra })
}

/** One assistant row that holds the given blocks. */
const assistant = (content: unknown[]) => ({ type: 'assistant', message: { type: 'message', role: 'assistant', content, stop_reason: null }, session_id: 'T-1' })

describe('classifyAmpMessage', () => {
  it('reads LeapMux\'s own user row', () => {
    expect(classify({ content: 'hello' })).toEqual({ kind: 'user_content' })
    expect(classify({ content: 'x', hidden: true })).toEqual({ kind: 'hidden' })
    expect(classify({ content: 'x', planExecution: true })).toEqual({ kind: 'plan_execution' })
  })

  it('reads a result row as the turn divider, a success and an error alike', () => {
    expect(classify({ type: 'result', subtype: 'success', is_error: false, result: 'done' })).toEqual({ kind: 'result_divider' })
    expect(classify({ type: 'result', subtype: 'error_during_execution', is_error: true, error: 'x' })).toEqual({ kind: 'result_divider' })
  })

  it('reads the tool rows by their side of the span', () => {
    expect(classify(ampToolUseRow('shell_command', { command: 'ls' }))).toEqual({ kind: 'tool_use' })
    expect(classify(ampToolResultRow('{"output":"","exitCode":0}'))).toEqual({ kind: 'tool_result' })
  })

  it('reads a retained call row as the result of a turn that stopped', () => {
    for (const completion of [MessageCompletion.INTERRUPTED, MessageCompletion.ERROR, MessageCompletion.COMPLETE])
      expect(classify(ampToolUseRow('shell_command', { command: 'sleep 40' }), { completion }), String(completion)).toEqual({ kind: 'tool_result' })
  })

  it('reads an assistant row by the block it holds', () => {
    expect(classify(assistant([{ type: 'text', text: 'Hello.' }]))).toEqual({ kind: 'assistant_text' })
    expect(classify(assistant([{ type: 'thinking', thinking: '**Planning**' }]))).toEqual({ kind: 'assistant_thinking' })
  })

  it('hides an assistant row that states nothing', () => {
    expect(classify(assistant([{ type: 'text', text: '  ' }]))).toEqual({ kind: 'hidden' })
    expect(classify(assistant([{ type: 'thinking', thinking: '' }]))).toEqual({ kind: 'hidden' })
    expect(classify(assistant([{ type: 'redacted_thinking', data: 'abc' }]))).toEqual({ kind: 'hidden' })
    expect(classify(assistant([]))).toEqual({ kind: 'hidden' })
  })

  it('reads a call before the text of a row the worker kept whole', () => {
    const whole = assistant([{ type: 'text', text: 'Running.' }, { type: 'tool_use', id: 'TU-1', name: 'shell_command', input: {} }])
    expect(classify(whole)).toEqual({ kind: 'tool_use' })
    expect(classify(assistant([{ type: 'thinking', thinking: 'Hmm.' }, { type: 'text', text: 'Yes.' }]))).toEqual({ kind: 'assistant_text' })
  })

  it('shows a block of a later Amp as the raw row', () => {
    expect(classify(assistant([{ type: 'image', source: {} }]))).toEqual({ kind: 'unknown' })
  })

  it('hides a user row that is not a tool result', () => {
    expect(classify({ type: 'user', message: { role: 'user', content: [{ type: 'text', text: 'echo' }] } })).toEqual({ kind: 'hidden' })
    expect(classify({ type: 'user', message: { role: 'user', content: 'a string' } })).toEqual({ kind: 'hidden' })
  })

  // A block with no call id states no call, so it owns no span and its row reads by
  // the blocks that remain.
  it('reads a call block with no id as no call', () => {
    expect(classify({ type: 'user', message: { role: 'user', content: [{ type: 'tool_result', content: 'x' }] } })).toEqual({ kind: 'hidden' })
    expect(classify(assistant([{ type: 'tool_use', name: 'shell_command', input: {} }, { type: 'text', text: 'Running.' }]))).toEqual({ kind: 'assistant_text' })
  })

  it('reads a row with no type and no string content as unknown', () => {
    expect(classify({ content: 42 })).toEqual({ kind: 'unknown' })
    expect(classify({ content: ['a'] })).toEqual({ kind: 'unknown' })
  })

  it('keeps a system line and an unknown line inspectable', () => {
    expect(classify({ type: 'system', subtype: 'compaction' })).toEqual({ kind: 'unknown' })
    expect(classify({ type: 'stream_event' })).toEqual({ kind: 'unknown' })
  })

  it('reads LeapMux\'s own notifications', () => {
    expect(classify({ type: 'agent_error', error: 'The Amp process exited unexpectedly' }).kind).toBe('notification')
    const thread = classify(undefined, {}, { old_seqs: [], messages: [{ type: 'context_cleared' }, { type: 'interrupted' }] })
    expect(thread.kind).toBe('notification')
  })

  it('hides an empty notification thread and reads a row with no payload as unknown', () => {
    expect(classify(undefined, {}, { old_seqs: [], messages: [] })).toEqual({ kind: 'hidden' })
    expect(classify(undefined)).toEqual({ kind: 'unknown' })
  })
})
