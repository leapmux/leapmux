import { describe, expect, it } from 'vitest'
import { executeAmpCodeFixture } from './ampCodeExecution'

describe('executeAmpCodeFixture', () => {
  it('computes output instead of accepting a precomputed result', () => {
    expect(executeAmpCodeFixture('text(40 + 2)')).toEqual({ status: 'done', result: '42' })
    expect(executeAmpCodeFixture('text(0); text(false); text(null); text("界"); text({answer: 6 * 7})')).toEqual({ status: 'done', result: '0\nfalse\nnull\n界\n{"answer":42}' })
  })

  it('preserves native empty output semantics', () => {
    expect(executeAmpCodeFixture('40 + 2')).toEqual({ status: 'done', result: '' })
    expect(executeAmpCodeFixture('text("")')).toEqual({ status: 'done', result: '' })
  })

  it('reports a computed script error in the captured native form', () => {
    expect(executeAmpCodeFixture('throw new Error("PROBE" + (70 + 7))')).toEqual({ status: 'error', error: { message: 'Error: PROBE77' } })
  })

  it.each([undefined, null, 0, false, '', '  '])('rejects invalid source: %j', (source) => {
    expect(executeAmpCodeFixture(source)).toMatchObject({ status: 'error', error: { message: expect.stringContaining('nonempty JavaScript') } })
  })

  it('reports syntax errors and keeps host tool access absent', () => {
    expect(executeAmpCodeFixture('const =')).toMatchObject({ status: 'error', error: { message: expect.stringContaining('SyntaxError') } })
    expect(executeAmpCodeFixture('require("node:fs")')).toMatchObject({ status: 'error', error: { message: expect.stringContaining('require is not defined') } })
  })

  it('stops a script that cannot finish', () => {
    expect(executeAmpCodeFixture('for (;;) {}')).toMatchObject({ status: 'error', error: { message: expect.stringContaining('Script execution timed out') } })
  })
})
