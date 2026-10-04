import { describe, expect, it } from 'vitest'
import { OH_MY_PI_EVENT } from '~/generated/contracts/ohmypi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyOhMyPiMessage } from './classification'
import '~/components/chat/providers'

function classify(parent: Record<string, unknown> | undefined, extra: Record<string, unknown> = {}) {
  return classifyOhMyPiMessage({ ...input(parent, undefined, AgentProvider.OH_MY_PI), ...extra })
}

describe('classifyOhMyPiMessage', () => {
  it('reads LeapMux\'s own user row', () => {
    expect(classify({ content: 'hello' })).toEqual({ kind: 'user_content' })
    expect(classify({ content: 'x', hidden: true })).toEqual({ kind: 'hidden' })
    expect(classify({ content: 'x', planExecution: true })).toEqual({ kind: 'plan_execution' })
  })

  it('reads the end of a run as the turn divider', () => {
    expect(classify({ type: 'agent_end', isTerminal: true, messages: [] })).toEqual({ kind: 'result_divider' })
  })

  it('reads the tool frames by their side of the span', () => {
    expect(classify({ type: 'tool_execution_start', toolCallId: 'c', toolName: 'bash', args: {} })).toEqual({ kind: 'tool_use' })
    expect(classify({ type: 'tool_execution_end', toolCallId: 'c', toolName: 'bash', result: {} })).toEqual({ kind: 'tool_result' })
    // A turn that ended while the call ran stores the start frame again as its end.
    expect(classify({ type: 'tool_execution_start', toolCallId: 'c', toolName: 'bash', args: {} }, { completion: MessageCompletion.INTERRUPTED })).toEqual({ kind: 'tool_result' })
  })

  it('reads a start frame as the result for each completion LeapMux records, and as the request for none', () => {
    const startFrame = { type: 'tool_execution_start', toolCallId: 'c', toolName: 'bash', args: {} }
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(classify(startFrame, { completion }), MessageCompletion[completion]).toEqual({ kind: 'tool_result' })
    expect(classify(startFrame, { completion: MessageCompletion.UNSPECIFIED })).toEqual({ kind: 'tool_use' })
  })

  it('hides an assistant message whose text is blank, and a message_end that holds no message', () => {
    expect(classify({ type: 'message_end', message: { role: 'assistant', content: [{ type: 'text', text: ' \n\t' }] } })).toEqual({ kind: 'hidden' })
    expect(classify({ type: 'message_end' })).toEqual({ kind: 'hidden' })
  })

  it('reads an assistant message by its text alone', () => {
    // The worker persists the thinking of each reply as a reasoning row of its own,
    // before the reply's row, so the reply's row draws its text and nothing else.
    const message = (content: unknown[]) => ({ type: 'message_end', message: { role: 'assistant', content } })
    expect(classify(message([{ type: 'text', text: 'Hello.' }]))).toEqual({ kind: 'assistant_text' })
    expect(classify(message([{ type: 'thinking', thinking: 'Greet.' }, { type: 'text', text: 'Hello.' }]))).toEqual({ kind: 'assistant_text' })
    expect(classify(message([{ type: 'thinking', thinking: 'Hmm.' }]))).toEqual({ kind: 'hidden' })
    expect(classify(message([{ type: 'thinking', thinking: '', thinkingSignature: 'x' }, { type: 'toolCall', id: 'c', name: 'bash' }]))).toEqual({ kind: 'hidden' })
    expect(classify(message([]))).toEqual({ kind: 'hidden' })
  })

  it('hides a user, tool result and shell echo message', () => {
    for (const role of ['user', 'toolResult', 'bashExecution'])
      expect(classify({ type: 'message_end', message: { role, content: [] } }), role).toEqual({ kind: 'hidden' })
  })

  it('reads a custom message as a notification and never as the reply, and a hidden one as nothing', () => {
    // omp writes a custom message for the model: a job's result, a late diagnostic, a
    // skill's file that the user asked for. None of them is the assistant's reply.
    const delivered = classify({ type: 'message_end', message: { role: 'custom', customType: 'async-result', content: 'x', display: true, details: { jobs: [{ jobId: 'A', label: 'A' }] } } })
    expect(delivered.kind).toBe('notification')
    expect(classify({ type: 'message_end', message: { role: 'custom', customType: 'launch-completion', content: 'Supervised process web exited with exit code 0.', display: true, attribution: 'agent' } }))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Supervised process web exited with exit code 0.' }] })
    expect(classify({ type: 'message_end', message: { role: 'custom', customType: 'skill-prompt', content: '# Release', display: true, details: { name: 'release' }, attribution: 'user' } }))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Loaded the skill release' }] })
    expect(classify({ type: 'message_end', message: { role: 'custom', content: 'Hidden.', display: false } })).toEqual({ kind: 'hidden' })
    expect(classify({ type: 'message_end', message: { role: 'custom', content: '  ' } })).toEqual({ kind: 'hidden' })
  })

  it('hides the copy of an agent-to-agent message, which its irc_message frame already draws', () => {
    // omp 18.2.11 (`irc-bridge.ts`) emits `irc_message` and then delivers the same
    // record to the conversation, which sends a message_end for it.
    expect(classify({ type: 'message_end', message: { role: 'custom', customType: 'irc:incoming', content: '<irc>...</irc>', display: true, details: { id: 'm1', from: 'A', message: 'Hi.' }, attribution: 'agent' } }))
      .toEqual({ kind: 'hidden' })
  })

  it('reads a notification frame', () => {
    const category = classify({ type: 'auto_retry_start', attempt: 1, maxAttempts: 3, delayMs: 10, errorMessage: 'x' })
    expect(category.kind).toBe('notification')
    expect(classify({ type: 'notice', message: '' })).toEqual({ kind: 'hidden' })
    expect(classify({ type: 'response', command: 'compact', success: true, data: {} }).kind).toBe('notification')
    expect(classify({ type: 'response', command: 'prompt', success: true })).toEqual({ kind: 'hidden' })
  })

  it('hides every frame the worker drops', () => {
    for (const type of ['agent_start', 'turn_start', 'turn_end', 'message_start', 'message_update', 'tool_execution_update', 'goal_updated', 'subagent_event', 'host_tool_call', 'extension_ui_response'])
      expect(classify({ type }), type).toEqual({ kind: 'hidden' })
  })

  it('reads a thread of notifications and hides an empty one', () => {
    const thread = classifyOhMyPiMessage(input(undefined, { old_seqs: [], messages: [{ type: 'auto_compaction_start' }, { type: 'auto_compaction_end', result: { tokensBefore: 10 } }] }, AgentProvider.OH_MY_PI))
    expect(thread).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'start' }, { kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 10 } }],
    })
    expect(classifyOhMyPiMessage(input(undefined, { old_seqs: [], messages: [] }, AgentProvider.OH_MY_PI))).toEqual({ kind: 'hidden' })
  })

  it('hides a thread of notifications that each read as no entry', () => {
    const blank = classifyOhMyPiMessage(input(undefined, { old_seqs: [], messages: [{ type: 'notice', message: '' }, { type: 'command_output', text: '  ' }] }, AgentProvider.OH_MY_PI))
    expect(blank).toEqual({ kind: 'hidden' })
  })

  it('decides every frame the contract lists, so none reaches the unknown card', () => {
    // A minimal frame of each type: the decision must not depend on a field that a
    // frame of that type can leave out.
    const unknown = Object.values(OH_MY_PI_EVENT).filter(type => classify({ type }).kind === 'unknown')
    expect(unknown, 'Classify each new omp frame: hide it, or read it as a row.').toEqual([])
  })

  it('reads LeapMux\'s own notification envelope', () => {
    expect(classify({ type: 'context_cleared' }).kind).toBe('notification')
  })

  it('reads a frame it does not know as unknown', () => {
    expect(classify({ type: 'hologram_update' })).toEqual({ kind: 'unknown' })
    expect(classify(undefined)).toEqual({ kind: 'unknown' })
  })
})
