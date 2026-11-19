import type { ControlPayloadFault } from './controlPrompt'
import { describe, expect, it } from 'vitest'
import { controlPayloadIsObject } from './controlPrompt'

describe('controlPayloadIsObject', () => {
  it('holds for a request that the Worker read, and for one that its provider does not recognize', () => {
    expect(controlPayloadIsObject(undefined)).toBe(true)
    expect(controlPayloadIsObject('unrecognized')).toBe(true)
  })

  it('fails for bytes that are not JSON, and for JSON that is not an object', () => {
    expect(controlPayloadIsObject('malformed')).toBe(false)
    expect(controlPayloadIsObject('not-an-object')).toBe(false)
  })

  // The types admit no other fault, but a stale bundle can still pass one.
  it('throws for a fault that the model does not state', () => {
    expect(() => controlPayloadIsObject('stale' as ControlPayloadFault)).toThrow('unhandled union member')
  })
})
