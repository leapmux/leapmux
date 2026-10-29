import { describe, expect, it } from 'vitest'
import { MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeFrameSnapshot, nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { openCodeCompactionEvents } from './compactionNotice'

const completed = {
  id: 'evt-native-compaction',
  type: 'session.compacted',
  properties: { sessionID: 'native-session' },
}
const thread = (...messages: unknown[]) => ({ type: 'notification_thread', messages })

describe('openCodeCompactionEvents', () => {
  it('reads the completed native event inside a stored notification thread', () => {
    const snapshot = nativeFrameSnapshot(thread({
      type: 'session.status',
      properties: {
        status: { type: 'idle' },
      },
    }, completed))
    expect(openCodeCompactionEvents(snapshot)).toEqual([completed])
  })

  it('preserves every completed event across notification threads', () => {
    const second = { ...completed, extra: 'second' }
    expect(openCodeCompactionEvents(nativeFrameSnapshot(thread(completed, second), thread(completed))))
      .toEqual([completed, second, completed])
  })

  it('requires both the stored row and the native event to belong to the session', () => {
    const foreign = {
      ...completed,
      properties: { sessionID: 'other-session' },
    }
    const snapshot = nativeOutputSnapshot([
      {
        frame: thread(completed),
        agentSessionId: 'other-session',
      },
      {
        frame: thread(foreign),
      },
      {
        frame: thread(completed),
      },
    ])
    expect(openCodeCompactionEvents(snapshot)).toEqual([completed])
  })

  it.each([MessageSource.USER, MessageSource.LEAPMUX])('ignores a matching payload from source %s', (source) => {
    const snapshot = nativeFrameSnapshot(thread(completed))
    for (const message of snapshot.messages)
      message.source = source
    expect(openCodeCompactionEvents(snapshot)).toEqual([])
  })

  it('does not search assistant or tool content for event tokens', () => {
    expect(openCodeCompactionEvents(nativeFrameSnapshot(
      {
        sessionUpdate: 'agent_message_chunk',
        content: {
          type: 'text',
          text: JSON.stringify(thread(completed)),
        },
      },
      {
        sessionUpdate: 'tool_call_update',
        rawOutput: thread(completed),
      },
      completed,
    ))).toEqual([])
  })

  it.each([
    null,
    [],
    { type: 'notification_thread' },
    { type: 'notification_thread', messages: null },
    { type: 'notification_thread', messages: {} },
    thread(),
    thread(null, 0, 'session.compacted'),
    thread({ type: 'session.compacted' }),
    thread({ type: 'session.compacted', properties: null }),
    thread({ type: 'session.compacted', properties: [] }),
    thread({
      type: 'session.compacted',
      properties: { sessionID: '' },
    }),
    thread({
      type: 'session.compacted',
      properties: { sessionID: 0 },
    }),
  ])('ignores a malformed or empty frame: %j', (frame) => {
    expect(openCodeCompactionEvents(nativeFrameSnapshot(frame))).toEqual([])
  })

  it('accepts an empty transcript and rejects an absent session identity', () => {
    const snapshot = nativeFrameSnapshot()
    expect(openCodeCompactionEvents(snapshot)).toEqual([])
    expect(() => openCodeCompactionEvents({ ...snapshot, agentId: ' ' })).toThrow('requires an agent and a native session identity')
    expect(() => openCodeCompactionEvents({ ...snapshot, agentSessionId: '' })).toThrow('requires an agent and a native session identity')
  })
})
