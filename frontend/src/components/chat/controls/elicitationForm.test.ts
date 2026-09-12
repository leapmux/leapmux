import { createStore } from 'solid-js/store'
import { describe, expect, it } from 'vitest'
import { buildElicitationResponse, createElicitationForm, elicitationFieldKey, elicitationURL } from './elicitationForm'

const key = elicitationFieldKey

describe('elicitation form', () => {
  it('validates schemas read from the reactive control store', () => {
    const [state] = createStore({ schema: { type: 'object', properties: { count: { type: 'integer' } } } })
    expect(createElicitationForm(state.schema).read({ [key('count')]: '0' })).toEqual({ content: { count: 0 } })
  })

  it('keeps an explicit empty default and leaves the provider schema unchanged', () => {
    const schema = Object.freeze({ type: 'object', required: ['label'], properties: { label: { type: 'string', default: '' } } })
    const form = createElicitationForm(schema)
    expect(form.read({})).toEqual({ content: { label: '' } })
    expect(Reflect.ownKeys(schema)).toEqual(['type', 'required', 'properties'])
  })

  it('refuses to silently round integers in numeric and structured fields', () => {
    const form = createElicitationForm({ type: 'object', properties: { count: { type: 'integer' }, data: { type: 'object' } } })
    expect(form.read({ [key('count')]: '9007199254740993' }).error).toBeTruthy()
    expect(form.read({ [key('data')]: '{"id":9007199254740993}' }).error).toBeTruthy()
  })

  it('preserves zero, false, empty strings, and optional omission', () => {
    const form = createElicitationForm({ type: 'object', properties: {
      count: { type: 'integer', minimum: 0, maximum: 3 },
      enabled: { type: 'boolean' },
      label: { type: 'string' },
      optional: { type: 'number' },
    }, required: ['count', 'enabled', 'label'] })
    expect(form.read({ [key('count')]: '0', [key('enabled')]: 'false', [key('label')]: '' })).toEqual({ content: { count: 0, enabled: false, label: '' } })
    expect(form.read({}).error).toBeTruthy()
    expect(form.read({ [key('count')]: '-1', [key('enabled')]: 'false', [key('label')]: '' }).error).toBeTruthy()
  })

  it('preserves enum wire values and supports multi-select defaults', () => {
    const form = createElicitationForm({ type: 'object', properties: {
      color: { type: 'string', oneOf: [{ const: 'b', title: 'Blue' }], default: 'b' },
      tags: { type: 'array', items: { type: 'string', enum: ['x', 'y'], enumNames: ['First', 'Second'] }, default: ['x'] },
    } })
    expect(form.fields[0].options).toEqual([{ value: '"b"', label: 'Blue' }])
    expect(form.fields[1].type).toBe('multiple')
    expect(form.read({})).toEqual({ content: { color: 'b', tags: ['x'] } })
    expect(form.read({ [key('color')]: '"unknown"' }).error).toBeTruthy()
  })

  it('validates nested schemas through JSON fields', () => {
    const form = createElicitationForm({ type: 'object', properties: { nested: { type: 'object', required: ['id'], properties: { id: { type: 'integer' } } } } })
    expect(form.read({ [key('nested')]: '{"id":0}' })).toEqual({ content: { nested: { id: 0 } } })
    expect(form.read({ [key('nested')]: '{"id":"0"}' }).error).toBeTruthy()
    expect(form.read({ [key('nested')]: '{broken' }).error).toBeTruthy()
  })

  it('keeps special property names as data', () => {
    const form = createElicitationForm(JSON.parse('{"type":"object","properties":{"__proto__":{"type":"string"}}}'))
    const result = form.read({ [key('__proto__')]: 'value' })
    expect(Object.hasOwn(result.content!, '__proto__')).toBe(true)
    expect(Object.getOwnPropertyDescriptor(result.content, '__proto__')?.value).toBe('value')
    expect(Object.getPrototypeOf(result.content)).toBe(Object.prototype)
  })

  it('rejects missing schemas and unresolved references without throwing', () => {
    expect(createElicitationForm(undefined).read({}).error).toBeTruthy()
    expect(createElicitationForm({ $ref: 'https://example.invalid/schema' }).read({ [key('')]: '{}' }).error).toBeTruthy()
  })

  it('accepts an empty object schema', () => {
    expect(createElicitationForm({ type: 'object', properties: {} }).read({})).toEqual({ content: {} })
  })

  it('validates string formats and lengths', () => {
    const form = createElicitationForm({ type: 'object', properties: { email: { type: 'string', format: 'email', maxLength: 20 } }, required: ['email'] })
    expect(form.read({ [key('email')]: 'a@example.com' }).content).toEqual({ email: 'a@example.com' })
    expect(form.read({ [key('email')]: 'invalid' }).error).toBeTruthy()
    expect(form.read({ [key('email')]: 'longaddress@example.com' }).error).toBeTruthy()
  })

  it('permits only explicit web links without embedded credentials', () => {
    expect(elicitationURL('https://example.com/form')).toBe('https://example.com/form')
    for (const value of [undefined, '', '/relative', 'javascript:alert(1)', 'file:///etc/passwd', 'https://user:pass@example.com'])
      expect(elicitationURL(value)).toBeUndefined()
  })

  it('keeps request IDs opaque and omits content on rejection', () => {
    expect(buildElicitationResponse('9007199254740993', 'accept', { count: 0 })).toMatchObject({ response: { request_id: '9007199254740993', response: { action: 'accept', content: { count: 0 } } } })
    expect(buildElicitationResponse('request', 'decline', { secret: 'unused' })).toMatchObject({ response: { response: { action: 'decline' } } })
    expect(JSON.stringify(buildElicitationResponse('request', 'decline', { secret: 'unused' }))).not.toContain('secret')
  })
})
