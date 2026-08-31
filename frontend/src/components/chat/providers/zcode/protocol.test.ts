import { describe, expect, it } from 'vitest'
import { ZCODE_DISPLAY, ZCODE_METHOD } from './protocol'

// The tables the FRONTEND alone reads. Both are wire literals the app-server sends,
// and neither has a Go twin -- `session/stop` is the only method of the three the
// worker also names, and `file_diff` appears in no Go file at all -- so a whole-table
// `toEqual` here is the only thing that would catch a silent rename.
//
// The tables BOTH sides read (events, tool kinds, tool names, modes, result types,
// decisions) are generated from contracts/zcode-protocol.json and are deliberately NOT
// pinned here: a `toEqual` against the generated module would compare it with a hand
// copy of itself, which is the second source the contract exists to remove.

describe('zcode interaction methods (ZCODE_METHOD)', () => {
  it('pins the methods the worker answers plus the stop frame', () => {
    expect(ZCODE_METHOD).toEqual({
      SessionStop: 'session/stop',
      RequestPermission: 'interaction/requestPermission',
      RequestUserInput: 'interaction/requestUserInput',
    })
  })
})

describe('zcode display kinds (ZCODE_DISPLAY)', () => {
  it('pins the one display kind that carries a structured patch', () => {
    expect(ZCODE_DISPLAY).toEqual({ FileDiff: 'file_diff' })
  })
})
