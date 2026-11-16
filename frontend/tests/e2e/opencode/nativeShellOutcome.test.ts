import { describe, expect, it } from 'vitest'
import { openCodeShellOutcome } from './nativeShellOutcome'
import { openCodeToolFrame, openCodeToolSnapshot } from './nativeToolRecords.fixtures'

describe('openCodeShellOutcome', () => {
  it.each([0, 7, -1])('reads native exit %s without inferring it from prose', (exitCode) => {
    expect(openCodeShellOutcome(openCodeToolSnapshot(openCodeToolFrame(undefined, {
      metadata: { exit: exitCode },
    })), 'native-call', 'native preview'))
      .toEqual({ text: 'native preview', exitCode, failed: exitCode !== 0 })
  })

  it.each([
    null,
    '7',
    false,
    0.5,
    Number.MAX_SAFE_INTEGER + 1,
    {},
    [],
  ])('rejects a malformed numeric exit %j', (exit) => {
    expect(() => openCodeShellOutcome(openCodeToolSnapshot(openCodeToolFrame(undefined, {
      metadata: { exit },
    })), 'native-call', 'native preview')).toThrow('safe integer exit code')
  })

  it('rejects absent status, incomplete results, duplicate results, and changed output', () => {
    expect(() => openCodeShellOutcome(openCodeToolSnapshot(openCodeToolFrame(undefined, { metadata: {} })), 'native-call', 'native preview')).toThrow('safe integer exit code')
    expect(() => openCodeShellOutcome(openCodeToolSnapshot(openCodeToolFrame('session.tool.progress')), 'native-call', 'native preview')).toThrow('one completed')
    const duplicate = openCodeToolSnapshot()
    duplicate.messages.push({ ...duplicate.messages[0]!, id: 'duplicate' })
    expect(() => openCodeShellOutcome(duplicate, 'native-call', 'native preview')).toThrow('one completed')
    expect(() => openCodeShellOutcome(openCodeToolSnapshot(), 'native-call', 'changed')).toThrow('different output bytes')
  })
})
