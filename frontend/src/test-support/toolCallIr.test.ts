import { describe, expect, it } from 'vitest'
import { toolCallIr } from './toolCallIr'

/**
 * The helper every kind test builds through must not be able to build what production
 * cannot.
 *
 * An override may state an explicit `undefined` on the six fields the helper
 * strips, so `{ images: undefined }` used to spread straight over the default and
 * produce a call with no `images` -- the shape that makes `imagesForIR` throw and
 * kills the whole row list. `toolCall` pulls the same three fields out of its
 * spread for the same reason.
 */
describe('toolCallIr', () => {
  it('keeps the defaults when an override states undefined', () => {
    const call = toolCallIr('read', { images: undefined, name: undefined, request: undefined })
    expect(call.images).toEqual([])
    expect(call.name).toBe('read')
    expect(call.request).toEqual({ path: '/p/a.ts' })
  })

  it('still applies an override that states a value', () => {
    const call = toolCallIr('read', { name: 'Read', request: { path: '/repo/a.ts' }, images: [] })
    expect(call.name).toBe('Read')
    expect(call.request).toEqual({ path: '/repo/a.ts' })
  })

  it('names the empty kind something a row can draw', () => {
    expect(toolCallIr('').name).toBe('tool')
  })

  // A COMPLETED call answers -- invariant I2 -- so the default result is the kind's
  // smallest one rather than nothing. A test that wants a call with no result states
  // a status that admits none.
  it('carries every field a call needs', () => {
    const call = toolCallIr('execute')
    expect(Object.keys(call).sort()).toEqual(['id', 'images', 'kind', 'name', 'request', 'result', 'status'])
    expect(call.result).toStrictEqual({ commands: [], unresolvedTerminals: [] })
  })

  it('leaves the result off a call that has not finished', () => {
    const call = toolCallIr('execute', { status: 'in_progress' })
    expect(call.result).toBeUndefined()
    expect(call.images).toStrictEqual([])
  })

  // The helper routes through the one validating builder, so a test cannot state a
  // call production is unable to produce -- which is the whole reason every other
  // test may trust the shapes it hands to a renderer.
  it('refuses to build a call that breaks an invariant', () => {
    expect(() => toolCallIr('read', { status: 'pending', result: { lines: null, fallbackContent: '' } }))
      .toThrow('result-before-the-call-finished')
    expect(() => toolCallIr('edit', { request: { changes: [] } })).toThrow('a-file-change-states-no-file')
  })
})
