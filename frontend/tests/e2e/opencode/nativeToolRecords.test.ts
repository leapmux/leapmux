import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openCodeNativeToolRecords, openCodeNativeToolRowId } from './nativeToolRecords'
import { openCodeToolFrame, openCodeToolSnapshot } from './nativeToolRecords.fixtures'

describe('openCodeNativeToolRowId', () => {
  it('uses the actual Worker span for the native model call', () => {
    const snapshot = openCodeToolSnapshot()
    snapshot.messages.push({
      ...snapshot.messages[0]!,
      id: 'opening',
      content: new TextEncoder().encode(JSON.stringify(openCodeToolFrame('session.tool.called'))),
    })
    expect(openCodeNativeToolRowId(snapshot, 'native-call')).toBe('worker-span')
  })

  it('rejects ambiguous repeated call IDs from different native messages', () => {
    const snapshot = openCodeToolSnapshot()
    snapshot.messages.push({
      ...snapshot.messages[0]!,
      id: 'other',
      spanId: 'other-span',
      content: new TextEncoder().encode(JSON.stringify(openCodeToolFrame(undefined, { assistantMessageID: 'msg_2' }))),
    })
    expect(() => openCodeNativeToolRowId(snapshot, 'native-call')).toThrow('2 Worker spans')
  })

  it('ignores another session and rejects missing native message identity', () => {
    expect(() => openCodeNativeToolRowId(openCodeToolSnapshot(openCodeToolFrame(undefined, { sessionID: 'foreign' })), 'native-call')).toThrow('0 Worker spans')
    expect(() => openCodeNativeToolRowId(openCodeToolSnapshot(openCodeToolFrame(undefined, { assistantMessageID: '' })), 'native-call')).toThrow('assistant message identity')
  })

  it.each([
    MessageCompletion.COMPLETE,
    MessageCompletion.INTERRUPTED,
    MessageCompletion.ERROR,
    MessageCompletion.FINISHED,
  ])('distinguishes retained opening and closing copies of a recovered part for %s', (completion) => {
    const frame = {
      type: 'tool',
      id: 'native-call',
      name: 'shell',
      state: {
        status: 'completed',
        metadata: { exit: 0 },
        content: [],
      },
    }
    expect(openCodeNativeToolRecords(openCodeToolSnapshot(frame), 'native-call')[0]?.finished).toBe(false)
    expect(openCodeNativeToolRecords(openCodeToolSnapshot(frame, completion), 'native-call')[0]?.finished).toBe(true)
  })
})
