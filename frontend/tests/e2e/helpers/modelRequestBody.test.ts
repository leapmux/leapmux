import { describe, expect, it } from 'vitest'
import { requestRows, requestToolDescriptors, toolDescriptor, toolInputSchema } from './modelRequestBody'

describe('requestRows', () => {
  const rows = [{ role: 'user', content: 'ROW' }]

  it.each([
    { protocol: 'openai-chat-completions', body: { messages: rows, input: 'OTHER', contents: 'OTHER' } },
    { protocol: 'openai-responses', body: { input: rows, messages: 'OTHER', contents: 'OTHER' } },
    { protocol: 'anthropic-messages', body: { messages: rows, input: 'OTHER', contents: 'OTHER' } },
    { protocol: 'google-generative-language', body: { contents: rows, messages: 'OTHER', input: 'OTHER' } },
  ] as const)('reads the rows field of $protocol without a copy', ({ protocol, body }) => {
    expect(requestRows(protocol, body)).toBe(rows)
  })

  it('keeps a Responses input string unchanged', () => {
    expect(requestRows('openai-responses', { input: 'DIRECT_PROMPT' })).toBe('DIRECT_PROMPT')
  })

  it.each([undefined, null, 'body', [], 0])('returns undefined for a body that is not an object: %j', (body) => {
    expect(requestRows('openai-chat-completions', body)).toBeUndefined()
  })

  it('returns undefined for the AWS event stream, whose service states no generic rows', () => {
    expect(requestRows('aws-event-stream', { messages: rows })).toBeUndefined()
  })
})

describe('toolDescriptor', () => {
  it('returns an entry that states its own name', () => {
    const entry = { name: 'direct', function: { name: 'nested' } }
    expect(toolDescriptor(entry)).toBe(entry)
  })

  it('returns the Chat Completions function object', () => {
    const fn = { name: 'chat_tool' }
    expect(toolDescriptor({ type: 'function', function: fn })).toBe(fn)
  })

  it('returns the custom object of a custom tool', () => {
    const custom = { name: 'apply_patch' }
    expect(toolDescriptor({ type: 'custom', custom })).toBe(custom)
  })

  it.each([
    { type: 'function', function: { name: null } },
    { type: 'function', function: 'chat_tool' },
    { type: 'other', custom: { name: 'apply_patch' } },
    { type: 'custom', custom: { name: undefined } },
    {},
  ])('returns an entry that states no name unchanged: %j', (entry) => {
    expect(toolDescriptor(entry)).toBe(entry)
  })
})

describe('requestToolDescriptors', () => {
  it('reads each descriptor out of its envelope in native order', () => {
    const fn = { name: 'chat_tool' }
    const direct = { name: 'anthropic_tool' }
    expect(requestToolDescriptors('openai-chat-completions', { tools: [{ type: 'function', function: fn }, direct] })).toEqual([fn, direct])
  })

  it('reads the Google function declarations of every tool entry', () => {
    const first = { name: 'read_file' }
    const second = { name: 'run_shell_command' }
    expect(requestToolDescriptors('google-generative-language', { tools: [{ functionDeclarations: [first] }, { functionDeclarations: [second] }] })).toEqual([first, second])
  })

  it.each([undefined, {}, { tools: null }, { tools: 'tool' }])('returns undefined for a body without a tools array: %j', (body) => {
    expect(requestToolDescriptors('anthropic-messages', body)).toBeUndefined()
  })

  it('returns an empty list for an empty catalog, which a strict reader then refuses', () => {
    expect(requestToolDescriptors('openai-responses', { tools: [] })).toEqual([])
  })

  it('returns undefined for the AWS event stream', () => {
    expect(requestToolDescriptors('aws-event-stream', { tools: [{ name: 'service_tool' }] })).toBeUndefined()
  })

  it.each([null, 'tool', [], 0])('refuses a catalog entry that is not an object: %j', (entry) => {
    expect(() => requestToolDescriptors('openai-chat-completions', { tools: [entry] })).toThrow('The native model tool catalog contains an invalid entry.')
  })
})

describe('toolInputSchema', () => {
  const schema = { type: 'object' }

  it.each([
    { label: 'Google', descriptor: { parametersJsonSchema: schema, parameters: 'OTHER', input_schema: 'OTHER' } },
    { label: 'OpenAI', descriptor: { parameters: schema, input_schema: 'OTHER' } },
    { label: 'Anthropic', descriptor: { input_schema: schema } },
  ])('reads the $label schema first', ({ descriptor }) => {
    expect(toolInputSchema(descriptor)).toBe(schema)
  })

  it('passes over a null field to the next one', () => {
    expect(toolInputSchema({ parameters: null, input_schema: schema })).toBe(schema)
  })

  it('returns undefined for a descriptor without a schema', () => {
    expect(toolInputSchema({ name: 'tool' })).toBeUndefined()
  })
})
