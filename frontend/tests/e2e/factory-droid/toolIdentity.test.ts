import type { NativeContextFixtures } from '../helpers/nativeScenario'
import { describe, expect, it, vi } from 'vitest'
import { droidToolRowId, droidToolRowIdFromFrames, scriptedDroidCall } from './toolIdentity'

vi.mock('../helpers/nativeMessages', () => ({
  nativeMessageBody: (value: unknown) => value,
  readNativeMessageSnapshot: async () => ({
    messages: [
      {
        type: 'tool_call',
        toolUse: {
          id: 'native-identity',
          name: 'Execute',
          input: { command: 'printf 0' },
        },
      },
    ],
  }),
}))

const expected = {
  id: 'call_source',
  name: 'Execute',
  arguments: { command: 'printf 0', enabled: false, count: 0 },
}
const frame = {
  type: 'tool_call',
  toolUse: { id: 'native-identity', name: expected.name, input: expected.arguments },
}

describe('scriptedDroidCall', () => {
  it('retains exact scripted call identity', () => {
    expect(scriptedDroidCall([expected], 'source')).toBe(expected)
  })
  it.each([
    [],
    [expected, expected],
  ].map(calls => ({ calls })))('refuses absent or duplicated identities %j', ({ calls }) => {
    expect(() => scriptedDroidCall(calls, 'source')).toThrow('one exact')
  })
})

describe('droidToolRowId', () => {
  it('keeps an already resolved native ID after verifying its Worker frame', async () => {
    const context = {
      modelScript: {
        status: async () => ({
          requests: [
            {
              response: {
                toolCalls: [expected],
              },
            },
          ],
        }),
      },
    } as unknown as NativeContextFixtures
    await expect(droidToolRowId(context, { agentId: 'agent', callId: 'native-identity' })).resolves.toBe('native-identity')
  })
})

describe('droidToolRowIdFromFrames', () => {
  it('keeps zero and false inputs and deduplicates observations of the same native call', () => {
    expect(droidToolRowIdFromFrames([null, frame, frame], expected)).toBe('native-identity')
  })
  it.each([
    [],
    [
      { ...frame, type: 'tool_result' },
    ],
    [
      {
        ...frame,
        toolUse: { ...frame.toolUse, name: 'Other' },
      },
    ],
    [
      {
        ...frame,
        toolUse: {
          ...frame.toolUse,
          input: { ...expected.arguments, count: 1 },
        },
      },
    ],
    [
      frame,
      {
        ...frame,
        toolUse: { ...frame.toolUse, id: 'another-native-call' },
      },
    ],
  ].map(frames => ({ frames })))('refuses absent, different, or ambiguous native calls %j', ({ frames }) => {
    expect(() => droidToolRowIdFromFrames(frames, expected)).toThrow('one exact')
  })
  it.each(['', null, 0])('refuses an invalid native ID %j', (id) => {
    expect(() => droidToolRowIdFromFrames([
      {
        ...frame,
        toolUse: { ...frame.toolUse, id },
      },
    ], expected)).toThrow('identity')
  })
})
