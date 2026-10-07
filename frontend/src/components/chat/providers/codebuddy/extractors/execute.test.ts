import { describe, expect, it } from 'vitest'
import { codebuddyCommandOutput } from './execute'

describe('codebuddyCommandOutput', () => {
  it.each(['', '0', 'false', 'actual output'])('preserves the native text renderer output %j', (value) => {
    expect(codebuddyCommandOutput({ _meta: { renderer: { type: 'text', value } } }, 'native record')).toBe(value)
  })

  it.each([undefined, {}, { _meta: { renderer: { type: 'other', value: 'not output' } } }, { _meta: { renderer: { type: 'text', value: 0 } } }])('keeps the original record for an absent or malformed renderer %#', (block) => {
    expect(codebuddyCommandOutput(block, 'native record')).toBeUndefined()
  })

  it('keeps the original empty failure record when the client supplies a generic output notice', () => {
    const text = 'Command: exit 7\nStdout: (empty)\nStderr: (empty)\nExit Code: 7\nSignal: (none)'
    expect(codebuddyCommandOutput({ _meta: { rawResponse: { exitCode: 7 }, renderer: { type: 'text', value: '(No output)' } } }, text)).toBeUndefined()
  })

  it('keeps the actual persisted preview rather than the renderer file notice', () => {
    expect(codebuddyCommandOutput({ _meta: { renderer: { type: 'text', value: 'file notice' } } }, '<persisted-output>\nnative preview\n</persisted-output>')).toBeUndefined()
  })
})
