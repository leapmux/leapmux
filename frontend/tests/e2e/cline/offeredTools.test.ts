import { describe, expect, it } from 'vitest'
import { offeredTools } from './offeredTools'

describe('offeredTools', () => {
  it('reads the offered names in their native order', () => {
    const body = { tools: [{ type: 'function', function: { name: 'switch_to_act_mode' } }, { type: 'function', function: { name: 'editor' } }] }
    expect(offeredTools(body)).toEqual(['switch_to_act_mode', 'editor'])
  })

  // A negative check such as `not.toContain('editor')` passed on these bodies before.
  it.each([undefined, null, {}, { tools: [] }, { tools: 'editor' }, { messages: [{ role: 'assistant', content: 'editor' }] }])('refuses a body without a nonempty catalog: %j', (body) => {
    expect(() => offeredTools(body)).toThrow('The native model request contains no nonempty tool catalog.')
  })

  it('refuses an entry without a name, rather than reading it as an empty name', () => {
    expect(() => offeredTools({ tools: [{ type: 'function', function: {} }] })).toThrow('The native model tool catalog contains an entry without a name.')
  })
})
