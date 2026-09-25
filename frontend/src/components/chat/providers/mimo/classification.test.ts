import { describe, expect, it } from 'vitest'
import { MIMO_EVENT } from '~/generated/contracts/mimo-protocol'
import { MESSAGE_METADATA_FIELD, NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { compactionFrame, errorFrame, mimoFrame, openingFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { input } from '../testUtils'
import { classifyMiMoMessage } from './classification'
import '~/components/chat/providers'

function classify(frame: Record<string, unknown> | undefined, completion?: MessageCompletion) {
  const parsed = input(frame, undefined, AgentProvider.MIMO_CODE)
  return classifyMiMoMessage({ ...parsed, ...(completion !== undefined ? { completion } : {}) })
}

describe('classifyMiMoMessage', () => {
  it('reads the idle status as the turn end', () => {
    expect(classify(statusFrame('idle'))).toEqual({ kind: 'result_divider' })
  })

  it('hides a busy status', () => {
    expect(classify(statusFrame('busy'))).toEqual({ kind: 'hidden' })
  })

  it('reads a retry as a notification', () => {
    expect(classify(statusFrame('retry', { attempt: 1, message: 'overloaded' }))).toMatchObject({ kind: 'notification' })
  })

  // The worker states the tool count beside every turn end and nowhere else, which is
  // what separates a failed turn's divider from a notification outside a turn.
  it('tells a failed turn from an error outside a turn', () => {
    expect(classify({ ...errorFrame('APIError', 'boom'), [MESSAGE_METADATA_FIELD.ToolUses]: 0 })).toEqual({ kind: 'result_divider' })
    expect(classify(errorFrame('APIError', 'boom'))).toMatchObject({ kind: 'notification' })
  })

  it('reads a compaction as a notification', () => {
    expect(classify(compactionFrame(false))).toMatchObject({ kind: 'notification' })
  })

  it('reads the two halves of a tool call', () => {
    expect(classify(openingFrame('bash', { command: 'ls' }))).toEqual({ kind: 'tool_use' })
    expect(classify(toolFrame('bash', { input: { command: 'ls' } }))).toEqual({ kind: 'tool_result' })
    // A call the turn cut short ends with its last frame, which still reads as running.
    expect(classify(openingFrame('bash', { command: 'ls' }), MessageCompletion.INTERRUPTED)).toEqual({ kind: 'tool_result' })
    expect(classify(toolFrame('bash', { status: 'pending' }))).toEqual({ kind: 'hidden' })
  })

  it('reads a LeapMux user row', () => {
    expect(classify({ content: 'hello' })).toEqual({ kind: 'user_content' })
    expect(classify({ content: 'plan', planExecution: true })).toEqual({ kind: 'plan_execution' })
    expect(classify({ content: 'x', hidden: true })).toEqual({ kind: 'hidden' })
  })

  it('reads a consolidated thread of notifications', () => {
    const parsed = input(undefined, { old_seqs: [1, 2], messages: [statusFrame('retry', { attempt: 1 }), statusFrame('retry', { attempt: 2 })] }, AgentProvider.MIMO_CODE)
    expect(classifyMiMoMessage(parsed)).toMatchObject({ kind: 'notification' })
    const empty = input(undefined, { old_seqs: [], messages: [] }, AgentProvider.MIMO_CODE)
    expect(classifyMiMoMessage(empty)).toEqual({ kind: 'hidden' })
  })

  it('reads a row it does not know as unknown', () => {
    expect(classify({ type: 'something.new', properties: {} })).toEqual({ kind: 'unknown' })
    expect(classify(undefined)).toEqual({ kind: 'unknown' })
  })

  // The worker reads `busy` for the turn flag and persists no other status but idle
  // and retry. A status of a later release states nothing a reader can draw.
  it('hides a status that states no known type', () => {
    expect(classify(statusFrame('compacting'))).toEqual({ kind: 'hidden' })
    expect(classify(mimoFrame(MIMO_EVENT.SessionStatus, { sessionID: TEST_SESSION }))).toEqual({ kind: 'hidden' })
  })

  it('reads the retry entry, not only its kind', () => {
    expect(classify(statusFrame('retry', { attempt: 3, message: 'overloaded' }))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'retry', scope: 'api', attempt: 3, error: 'overloaded' }],
    })
  })

  // A part update that is neither a compaction nor a tool part carries nothing the
  // transcript draws: text and reasoning reach it as the worker's assembled rows.
  it('hides a part update that is not a tool or a compaction', () => {
    expect(classify(mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION, part: { type: 'text', text: 'Hello' } }))).toEqual({ kind: 'hidden' })
    expect(classify(mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION, part: { type: 'tool', tool: 'bash', state: { status: 'completed' } } }))).toEqual({ kind: 'hidden' })
    expect(classify(mimoFrame(MIMO_EVENT.MessagePartUpdated, { sessionID: TEST_SESSION }))).toEqual({ kind: 'hidden' })
  })

  // `hidden` wins over the plan flag: a row the worker hid stays hidden.
  it('hides a hidden user row that also states a plan execution', () => {
    expect(classify({ content: 'plan', planExecution: true, hidden: true })).toEqual({ kind: 'hidden' })
  })

  it('reads a user row whose content is not text as unknown', () => {
    expect(classify({ content: ['hello'] })).toEqual({ kind: 'unknown' })
  })

  // A row in LeapMux's own envelope carries no MiMo event, and every provider draws
  // it the same way.
  it('reads a LeapMux notification row as a notification', () => {
    expect(classify({ type: NOTIFICATION_TYPE.ContextCleared })).toMatchObject({ kind: 'notification' })
    expect(classify({ type: NOTIFICATION_TYPE.Interrupted })).toMatchObject({ kind: 'notification' })
  })

  // Only a failed turn's error states the tool count. A count that is not a number is
  // no count, so the error is a notification outside a turn.
  it('reads an error with a tool count that is not a number as a notification', () => {
    expect(classify({ ...errorFrame('APIError', 'boom'), [MESSAGE_METADATA_FIELD.ToolUses]: '0' })).toMatchObject({ kind: 'notification' })
  })

  it('hides an error outside a turn that states no words', () => {
    expect(classify(mimoFrame(MIMO_EVENT.SessionError, { sessionID: TEST_SESSION, error: {} }))).toEqual({ kind: 'hidden' })
  })

  it('reads a consolidated thread of compactions and errors', () => {
    const parsed = input(undefined, { old_seqs: [1, 2], messages: [compactionFrame(false), compactionFrame(true), errorFrame('APIError', 'boom')] }, AgentProvider.MIMO_CODE)
    expect(classifyMiMoMessage(parsed)).toEqual({
      kind: 'notification',
      entries: [
        { kind: 'compaction', phase: 'start', detail: { trigger: 'manual' } },
        { kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } },
        { kind: 'text', text: 'MiMo reported an error: APIError: boom' },
      ],
    })
  })

  // A thread whose members state nothing the reader draws is hidden rather than
  // drawn as an empty notification row.
  it('hides a thread whose members state no entry', () => {
    const parsed = input(undefined, { old_seqs: [1], messages: [mimoFrame(MIMO_EVENT.SessionError, { sessionID: TEST_SESSION, error: {} })] }, AgentProvider.MIMO_CODE)
    expect(classifyMiMoMessage(parsed)).toEqual({ kind: 'hidden' })
  })
})
