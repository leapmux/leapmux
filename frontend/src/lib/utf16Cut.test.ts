import { describe, expect, it } from 'vitest'
import { snapUtf16CutBackward, snapUtf16CutForward } from './utf16Cut'

describe('cuts at UTF-16 boundaries', () => {
  it('moves a cut between surrogate halves in the requested direction', () => {
    const text = 'a😀b'

    expect(snapUtf16CutBackward(text, 2)).toBe(1)
    expect(snapUtf16CutForward(text, 2)).toBe(3)
  })

  it('clamps an out-of-range cut', () => {
    expect(snapUtf16CutBackward('abc', -1)).toBe(0)
    expect(snapUtf16CutForward('abc', 9)).toBe(3)
  })
})
