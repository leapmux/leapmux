import { describe, expect, it } from 'vitest'
import { openCodeToolFrame, openCodeToolSnapshot } from './nativeToolRecords.fixtures'
import { readOpenCodeNativeOutput } from './outputFilePaths'

describe('readOpenCodeNativeOutput', () => {
  it('reads the native truncated shell footer when metadata omits the pointer', () => {
    const frame = openCodeToolFrame(undefined, {
      metadata: { truncated: true },
      content: [
        { type: 'text', text: 'tail\n\n[showing lines 9-10 of 10; full output saved to /native/sh_1.out]' },
      ],
    })
    expect(readOpenCodeNativeOutput(openCodeToolSnapshot(frame), 'native-call').paths).toEqual(['/native/sh_1.out'])
  })

  it('reads the original native pointer and preview without supplements', () => {
    const snapshot = openCodeToolSnapshot()
    const before = JSON.stringify(snapshot, (_key, value: unknown) => typeof value === 'bigint'
      ? value.toString()
      : value)
    expect(readOpenCodeNativeOutput(snapshot, 'native-call')).toMatchObject({
      paths: ['/native/shell/output'],
      previewText: 'native preview',
    })
    expect(JSON.stringify(snapshot, (_key, value: unknown) => typeof value === 'bigint'
      ? value.toString()
      : value)).toBe(before)
  })

  it.each([
    'https://example.com/output',
    'file:///output',
    'relative/output',
    ' ',
    '/bad\0path',
    null,
  ])('rejects a non-filesystem pointer %j', (outputPath) => {
    expect(() => readOpenCodeNativeOutput(openCodeToolSnapshot(openCodeToolFrame(undefined, {
      metadata: { outputPath },
    })), 'native-call')).toThrow('filesystem output pointer')
  })

  it('rejects absent and duplicate final frames', () => {
    expect(() => readOpenCodeNativeOutput(openCodeToolSnapshot(openCodeToolFrame('session.tool.called')), 'native-call')).toThrow('one completed')
    const snapshot = openCodeToolSnapshot()
    snapshot.messages.push({ ...snapshot.messages[0]!, id: 'duplicate' })
    expect(() => readOpenCodeNativeOutput(snapshot, 'native-call')).toThrow('one completed')
  })
})
