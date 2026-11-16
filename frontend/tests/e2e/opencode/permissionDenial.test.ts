import { describe, expect, it } from 'vitest'
import { openCodeToolFrame, openCodeToolSnapshot } from './nativeToolRecords.fixtures'
import { OPENCODE_PERMISSION_REJECTION, openCodeToolEnding } from './permissionDenial'

describe('openCodeToolEnding', () => {
  it('reads the exact native refusal without inventing content blocks', () => {
    const frame = openCodeToolFrame('session.tool.failed', {
      content: [],
      error: { type: 'aborted', message: OPENCODE_PERMISSION_REJECTION },
    })
    expect(openCodeToolEnding(openCodeToolSnapshot(frame), 'native-call')).toEqual({ status: 'failed', text: [], error: OPENCODE_PERMISSION_REJECTION })
  })

  it('rejects progress and another native session', () => {
    expect(() => openCodeToolEnding(openCodeToolSnapshot(openCodeToolFrame('session.tool.progress')), 'native-call')).toThrow('0 final frames')
    expect(() => openCodeToolEnding(openCodeToolSnapshot(openCodeToolFrame(undefined, { sessionID: 'other' })), 'native-call')).toThrow('0 final frames')
  })
})
