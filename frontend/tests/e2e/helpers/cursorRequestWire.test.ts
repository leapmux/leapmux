import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { cursorRunRequestWitness } from './cursorRequestWire'
import { encodeLengthDelimited, encodeStringField, encodeVarint } from './cursorWire'

const concat = (parts: Uint8Array[]) => new Uint8Array(Buffer.concat(parts.map(part => Buffer.from(part))))
const scalar = (field: number, value: number) => concat([encodeVarint(field * 8), encodeVarint(value)])
const run = (fields: Uint8Array[]) => encodeLengthDelimited(1, concat(fields))
const userMode = (mode?: number) => encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, concat([encodeStringField(1, 'Actual native prompt.'), ...(mode !== undefined ? [scalar(4, mode)] : [])]))))

describe('cursorRunRequestWitness', () => {
  it('reads actual selected project rules without deriving them from the user prompt', () => {
    const rule = (path: string, content: string) => encodeLengthDelimited(10, encodeLengthDelimited(1, concat([encodeStringField(1, path), encodeStringField(2, content)])))
    const selected = encodeLengthDelimited(3, concat([rule('/private/project/AGENTS.md', 'NATIVE_PROJECT_CONFIG'), rule('/private/project/.cursor/rules/empty.mdc', '')]))
    const user = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, concat([encodeStringField(1, 'User prompt without the project marker.'), selected]))))
    expect(cursorRunRequestWitness(run([user]))).toEqual({ mode: 0, cursorRules: [{ path: '/private/project/AGENTS.md', content: 'NATIVE_PROJECT_CONFIG' }, { path: '/private/project/.cursor/rules/empty.mdc', content: '' }] })
  })

  it('keeps an absent project context distinct from an empty selected rule list', () => {
    expect(cursorRunRequestWitness(run([userMode(1)]))).not.toHaveProperty('cursorRules')
    const user = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, encodeLengthDelimited(3, new Uint8Array()))))
    expect(cursorRunRequestWitness(run([user]))).toEqual({ mode: 0, cursorRules: [] })
  })

  it('rejects an absent or malformed native selected rule message', () => {
    const selected = encodeLengthDelimited(3, encodeLengthDelimited(10, new Uint8Array()))
    const user = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, selected)))
    expect(() => cursorRunRequestWitness(run([user]))).toThrow('contains no rule message')
    const validRule = encodeLengthDelimited(1, encodeStringField(2, 'actual rule'))
    const validSelected = encodeLengthDelimited(3, encodeLengthDelimited(10, validRule))
    const validUser = encodeLengthDelimited(2, encodeLengthDelimited(1, encodeLengthDelimited(1, validSelected)))
    const complete = run([validUser])
    expect(() => cursorRunRequestWitness(complete.subarray(0, complete.length - 1))).toThrow('truncated')
  })
  it('preserves the actual requested model, native parameters, and Plan enum', () => {
    const requested = encodeLengthDelimited(9, concat([
      encodeStringField(1, 'actual-model'),
      scalar(2, 1),
      scalar(7, 1),
      encodeLengthDelimited(3, concat([encodeStringField(1, 'reasoning_effort'), encodeStringField(2, 'low')])),
      encodeLengthDelimited(3, concat([encodeStringField(1, 'context'), encodeStringField(2, '256k')])),
    ]))
    expect(cursorRunRequestWitness(run([requested, userMode(3)]))).toEqual({ requestedModel: { modelId: 'actual-model', maxMode: true, builtInModel: true, parameters: [{ id: 'reasoning_effort', value: 'low' }, { id: 'context', value: '256k' }] }, mode: 3 })
  })

  it('keeps native requested and catalog model details separate', () => {
    const frame = run([encodeLengthDelimited(9, encodeStringField(1, 'native-requested')), encodeLengthDelimited(3, encodeStringField(1, 'native-details'))])
    expect(cursorRunRequestWitness(frame)).toEqual({ requestedModel: { modelId: 'native-requested', maxMode: false, builtInModel: false, parameters: [] }, modelDetails: { modelId: 'native-details' } })
  })

  it('retains optional detail false and requested proto3 false defaults', () => {
    expect(cursorRunRequestWitness(run([encodeLengthDelimited(9, new Uint8Array()), encodeLengthDelimited(3, concat([encodeStringField(1, 'native-details'), scalar(7, 0)]))]))).toEqual({ requestedModel: { modelId: '', maxMode: false, builtInModel: false, parameters: [] }, modelDetails: { modelId: 'native-details', maxMode: false } })
  })

  it.each([0, 1, 2, 3, 8, 99])('preserves the actual native mode value: %s', (mode) => {
    expect(cursorRunRequestWitness(run([userMode(mode)]))?.mode).toBe(mode)
  })

  it('uses an unspecified mode only when the actual UserMessage omits its enum', () => {
    expect(cursorRunRequestWitness(run([userMode()]))).toEqual({ mode: 0 })
    expect(cursorRunRequestWitness(run([]))).toEqual({})
  })

  it('does not create a requested model or mode from an unrelated native message', () => {
    expect(cursorRunRequestWitness(encodeLengthDelimited(2, encodeStringField(15, 'other-execution')))).toBeUndefined()
  })

  it('rejects malformed native booleans and truncated native fields', () => {
    expect(() => cursorRunRequestWitness(run([encodeLengthDelimited(9, scalar(2, 2))]))).toThrow('must be zero or one')
    const complete = run([encodeLengthDelimited(9, encodeStringField(1, 'actual-model'))])
    expect(() => cursorRunRequestWitness(complete.subarray(0, complete.length - 1))).toThrow('truncated')
  })
})
