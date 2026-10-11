import { describe, expect, it } from 'vitest'
import { REASONIX_METHOD } from '~/generated/contracts/reasonix-protocol'
import { reasonixElicitation } from './elicitation'

describe('reasonixElicitation', () => {
  it('reads zero and false values from a native form schema', () => {
    const schema = { type: 'object', properties: { count: { type: 'integer', minimum: 0 }, enabled: { type: 'boolean', default: false } } }
    expect(reasonixElicitation({
      method: REASONIX_METHOD.McpRequestInteraction,
      params: { mode: 'form', message: 'Choose values.', server: 'form_probe', requestedSchema: schema },
    })).toEqual({ mode: 'form', message: 'Choose values.', server: 'form_probe', schema, url: '', title: '', description: '' })
  })

  it('reads a native URL request', () => {
    expect(reasonixElicitation({
      method: REASONIX_METHOD.McpRequestInteraction,
      params: { mode: 'url', message: 'Sign in.', server: 'auth', url: 'https://example.com/login' },
    })).toMatchObject({ mode: 'url', message: 'Sign in.', server: 'auth', url: 'https://example.com/login' })
  })

  it('still reads the standard ACP form', () => {
    expect(reasonixElicitation({ method: 'elicitation/create', params: { mode: 'form', message: 'Standard form.' } })?.message).toBe('Standard form.')
    expect(reasonixElicitation({ method: 'session/request_permission', params: { message: 'Not a form.' } })).toBeUndefined()
  })
})
