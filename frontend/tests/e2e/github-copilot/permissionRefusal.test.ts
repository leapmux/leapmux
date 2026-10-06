import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputRow } from '../helpers/nativeOutputReaderCases'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_METHOD } from '../../../src/generated/contracts/copilot-protocol'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { COPILOT_USER_REJECTION, copilotToolCompletion } from './permissionRefusal'

/** One stored session event notification, as the Worker keeps it. */
function eventFrame(type: string, data: Record<string, unknown>, overrides: { sessionId?: string, agentId?: string } = {}) {
  return {
    jsonrpc: '2.0',
    method: COPILOT_METHOD.SessionEvent,
    params: {
      sessionId: overrides.sessionId ?? 'native-session',
      event: { id: 'event-1', type, ...(overrides.agentId === undefined ? {} : { agentId: overrides.agentId }), data },
    },
  }
}

const rejected = { toolCallId: 'denied-1', success: false, error: { message: 'The user rejected this tool call', code: 'rejected' } }

/** One stored row of the native session. The row belongs to the span of the refused call. */
function row(frame: unknown, overrides: { agentSessionId?: string } = {}): NativeOutputRow {
  return { frame, spanId: 'denied-1', ...overrides }
}

function snapshot(...rows: NativeOutputRow[]): NativeMessageSnapshot {
  return nativeOutputSnapshot(rows, { agentId: 'parent', agentSessionId: 'native-session' })
}

describe('COPILOT_USER_REJECTION', () => {
  it.each(['The user rejected this tool call', 'The user rejected this tool call.'])('accepts the native rejection text %j', (text) => {
    expect(text).toMatch(COPILOT_USER_REJECTION)
  })

  it.each([
    'The user rejected this tool call. User feedback: use another file',
    'Permission denied and could not request permission from user',
    'Permission to run this tool was denied by a deny rule.',
  ])('refuses a different native refusal %j', (text) => {
    expect(text).not.toMatch(COPILOT_USER_REJECTION)
  })
})

describe('copilotToolCompletion', () => {
  it('reads the exact native completion of a rejected tool call', () => {
    expect(copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected))), 'denied-1')).toEqual({
      success: false,
      error: { message: 'The user rejected this tool call', code: 'rejected' },
    })
  })

  it('skips the start event of the same call', () => {
    const start = row(eventFrame(COPILOT_EVENT.ToolStarted, { toolCallId: 'denied-1', toolName: 'bash', arguments: {} }))
    expect(copilotToolCompletion(snapshot(start, row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected))), 'denied-1').success).toBe(false)
  })

  it('reads a successful completion that has no error', () => {
    expect(copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'denied-1', success: true }))), 'denied-1')).toEqual({ success: true })
  })

  it('ignores a completion of the same call ID in another native session', () => {
    const foreign = row(eventFrame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'denied-1', success: true }, { sessionId: 'other' }), { agentSessionId: 'other' })
    expect(copilotToolCompletion(snapshot(foreign, row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected))), 'denied-1').success).toBe(false)
  })

  it('refuses an absent completion', () => {
    expect(() => copilotToolCompletion(snapshot(), 'denied-1')).toThrow('has 0 completions')
  })

  it('refuses two completions for one call', () => {
    const frame = eventFrame(COPILOT_EVENT.ToolCompleted, rejected)
    expect(() => copilotToolCompletion(snapshot(row(frame), row(frame)), 'denied-1')).toThrow('has 2 completions')
  })

  it('refuses a completion that identifies another call or session', () => {
    expect(() => copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, { ...rejected, toolCallId: 'other' }))), 'denied-1')).toThrow('another session or call')
    expect(() => copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected, { sessionId: 'other' }))), 'denied-1')).toThrow('another session or call')
  })

  it('refuses a completion of a subagent', () => {
    expect(() => copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected, { agentId: 'child-1' }))), 'denied-1')).toThrow('belongs to a subagent')
  })

  it('refuses a completion without a boolean success value', () => {
    expect(() => copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'denied-1', success: 'false' }))), 'denied-1')).toThrow('no success value')
  })

  it.each([
    { label: 'a null error', error: null },
    { label: 'an error without a message', error: { code: 'rejected' } },
    { label: 'an error with a numeric code', error: { message: 'The user rejected this tool call', code: 7 } },
  ])('refuses $label', ({ error }) => {
    expect(() => copilotToolCompletion(snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, { toolCallId: 'denied-1', success: false, error }))), 'denied-1')).toThrow('malformed error')
  })

  it('requires an exact call and a nonempty agent and session identity', () => {
    const source = snapshot(row(eventFrame(COPILOT_EVENT.ToolCompleted, rejected)))
    expect(() => copilotToolCompletion(source, '')).toThrow('exact agent, session, and call ID')
    expect(() => copilotToolCompletion({ ...source, agentId: '' }, 'denied-1')).toThrow('exact agent, session, and call ID')
    expect(() => copilotToolCompletion({ ...source, agentSessionId: ' ' }, 'denied-1')).toThrow('exact agent, session, and call ID')
  })
})
