import { describe, expect, it } from 'vitest'
import { assertNativeSoundActivity } from './nativeTurnEndSound'

describe('assertNativeSoundActivity', () => {
  it('keeps a text-only script distinct from an actual answer tool', () => {
    expect(() => assertNativeSoundActivity([{ text: 'A text-only answer.' }], false)).not.toThrow()
    expect(() => assertNativeSoundActivity([{ toolCalls: [{ id: 'answer', name: 'answer', arguments: {} }] }], true)).not.toThrow()
  })

  it('rejects contradictory activity and an empty script before browser state changes', () => {
    expect(() => assertNativeSoundActivity([{ text: 'A text-only answer.' }], true)).toThrow('expected tool activity')
    expect(() => assertNativeSoundActivity([{ toolCalls: [{ id: 'answer', name: 'answer', arguments: {} }] }], false)).toThrow('expected tool activity')
    expect(() => assertNativeSoundActivity([], false)).toThrow('needs a model step')
  })
})
