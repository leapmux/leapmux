import { describe, expect, it } from 'vitest'
import { assertNativeSoundActivity } from './nativeTurnEndSound'

describe('assertNativeSoundActivity', () => {
  it('keeps a text-only script distinct from an actual answer tool', () => {
    expect(() => assertNativeSoundActivity([{ text: 'A text-only answer.' }], false)).not.toThrow()
    expect(() => assertNativeSoundActivity([{ toolCalls: [{ id: 'answer', name: 'answer', arguments: {} }] }], true)).not.toThrow()
  })

  it('counts no tool activity for a call that only delivers the provider answer', () => {
    const answer = { id: 'answer', name: 'answer', arguments: {} }
    expect(() => assertNativeSoundActivity([{ toolCalls: [answer] }], false, ['answer'])).not.toThrow()
    expect(() => assertNativeSoundActivity([{ toolCalls: [answer] }], true, ['answer'])).toThrow('expected tool activity')
    const bash = { id: 'bash', name: 'bash', arguments: {} }
    expect(() => assertNativeSoundActivity([{ toolCalls: [bash] }, { toolCalls: [answer] }], true, ['answer'])).not.toThrow()
    expect(() => assertNativeSoundActivity([{ toolCalls: [bash] }, { toolCalls: [answer] }], false, ['answer'])).toThrow('expected tool activity')
  })

  it('rejects contradictory activity and an empty script before browser state changes', () => {
    expect(() => assertNativeSoundActivity([{ text: 'A text-only answer.' }], true)).toThrow('expected tool activity')
    expect(() => assertNativeSoundActivity([{ toolCalls: [{ id: 'answer', name: 'answer', arguments: {} }] }], false)).toThrow('expected tool activity')
    expect(() => assertNativeSoundActivity([], false)).toThrow('needs a model step')
  })
})
