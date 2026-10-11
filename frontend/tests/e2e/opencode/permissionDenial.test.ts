import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { NativeOutputRow } from '../helpers/nativeOutputReaderCases'
import { describe, expect, it } from 'vitest'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { OPENCODE_PERMISSION_REJECTION, openCodeToolEnding } from './permissionDenial'

/** The failed frame that OpenCode sends for a call whose permission the reader rejected. */
const rejectedFrame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'denied-1',
  status: 'failed',
  kind: 'execute',
  content: [{ type: 'content', content: { type: 'text', text: OPENCODE_PERMISSION_REJECTION } }],
  rawOutput: { error: OPENCODE_PERMISSION_REJECTION },
}

/** One stored row of the native session. The row belongs to the span of the refused call. */
function row(frame: unknown, overrides: { agentSessionId?: string } = {}): NativeOutputRow {
  return { frame, spanId: 'denied-1', ...overrides }
}

function snapshot(...rows: NativeOutputRow[]): NativeMessageSnapshot {
  return nativeOutputSnapshot(rows, { agentId: 'parent', agentSessionId: 'native-session' })
}

describe('openCodeToolEnding', () => {
  it('reads the exact native refusal of a rejected permission', () => {
    expect(openCodeToolEnding(snapshot(row(rejectedFrame)), 'denied-1')).toEqual({
      status: 'failed',
      text: [OPENCODE_PERMISSION_REJECTION],
      error: OPENCODE_PERMISSION_REJECTION,
    })
  })

  it('skips the opening call and the progress updates of the same call', () => {
    const opening = { sessionUpdate: 'tool_call', toolCallId: 'denied-1', status: 'pending' }
    const progress = { sessionUpdate: 'tool_call_update', toolCallId: 'denied-1', status: 'in_progress' }
    expect(openCodeToolEnding(snapshot(row(opening), row(progress), row(rejectedFrame)), 'denied-1').status).toBe('failed')
  })

  it('ignores a final frame of the same call ID in another native session', () => {
    const foreign = row({ ...rejectedFrame, status: 'completed', rawOutput: { output: 'FOREIGN' } }, { agentSessionId: 'other-session' })
    expect(openCodeToolEnding(snapshot(foreign, row(rejectedFrame)), 'denied-1').status).toBe('failed')
  })

  it('omits the error of a frame whose raw output has none', () => {
    const completed = { ...rejectedFrame, status: 'completed', content: [], rawOutput: { output: '' } }
    expect(openCodeToolEnding(snapshot(row(completed)), 'denied-1')).toEqual({ status: 'completed', text: [] })
  })

  it('refuses an absent final frame', () => {
    expect(() => openCodeToolEnding(snapshot(), 'denied-1')).toThrow('has 0 final frames')
  })

  it('refuses two final frames for one call', () => {
    expect(() => openCodeToolEnding(snapshot(row(rejectedFrame), row(rejectedFrame)), 'denied-1')).toThrow('has 2 final frames')
  })

  it('refuses a frame in the call span that identifies another call', () => {
    expect(() => openCodeToolEnding(snapshot(row({ ...rejectedFrame, toolCallId: 'other-call' })), 'denied-1')).toThrow('identifies another call')
  })

  it('refuses a frame that holds no object', () => {
    expect(() => openCodeToolEnding(snapshot(row(['not', 'an', 'object'])), 'denied-1')).toThrow('must contain an object')
  })

  it('requires an exact call and a nonempty agent and session identity', () => {
    const source = snapshot(row(rejectedFrame))
    expect(() => openCodeToolEnding(source, '')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeToolEnding({ ...source, agentId: ' ' }, 'denied-1')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeToolEnding({ ...source, agentSessionId: '' }, 'denied-1')).toThrow('exact agent, session, and call ID')
  })
})
