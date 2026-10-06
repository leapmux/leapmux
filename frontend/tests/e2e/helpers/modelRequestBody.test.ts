import { describe, expect, it } from 'vitest'
import { isSystemRow, requestRows, requestSystemFields, requestToolDescriptors, rowContent, toolDescriptor, toolInputSchema } from './modelRequestBody'

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

describe('rowContent', () => {
  it.each([
    { protocol: 'openai-chat-completions', row: { content: 'ROW', parts: 'OTHER' } },
    { protocol: 'openai-responses', row: { content: 'ROW', parts: 'OTHER' } },
    { protocol: 'anthropic-messages', row: { content: 'ROW', parts: 'OTHER' } },
    { protocol: 'google-generative-language', row: { parts: 'ROW', content: 'OTHER' } },
  ] as const)('reads the content field of a $protocol row', ({ protocol, row }) => {
    expect(rowContent(protocol, row)).toBe('ROW')
  })

  it('returns undefined for a row that states no content', () => {
    expect(rowContent('anthropic-messages', { role: 'user' })).toBeUndefined()
  })
})

describe('isSystemRow', () => {
  it.each(['openai-chat-completions', 'openai-responses'] as const)('takes a system row and a developer row of %s', (protocol) => {
    expect(isSystemRow(protocol, { role: 'system' })).toBe(true)
    expect(isSystemRow(protocol, { role: 'developer' })).toBe(true)
    expect(isSystemRow(protocol, { role: 'user' })).toBe(false)
    expect(isSystemRow(protocol, { role: 'assistant' })).toBe(false)
    expect(isSystemRow(protocol, {})).toBe(false)
  })

  // Claude Code states its Plan instructions in a system row of `messages`, beside the top-level `system` field.
  it('takes a system row of anthropic-messages, and no developer row', () => {
    expect(isSystemRow('anthropic-messages', { role: 'system' })).toBe(true)
    expect(isSystemRow('anthropic-messages', { role: 'developer' })).toBe(false)
    expect(isSystemRow('anthropic-messages', { role: 'user' })).toBe(false)
    expect(isSystemRow('anthropic-messages', { role: 'assistant' })).toBe(false)
    expect(isSystemRow('anthropic-messages', {})).toBe(false)
  })

  it('takes no row of google-generative-language, which keeps its system instructions outside the rows', () => {
    expect(isSystemRow('google-generative-language', { role: 'system' })).toBe(false)
    expect(isSystemRow('google-generative-language', { role: 'developer' })).toBe(false)
  })
})

describe('requestSystemFields', () => {
  const blocks = [{ type: 'text', text: 'SYSTEM' }]
  const parts = [{ text: 'SYSTEM' }]
  const body = { instructions: 'INSTRUCTIONS', system: blocks, systemInstruction: { parts }, messages: [{ role: 'system', content: 'ROW' }] }

  it.each([
    { protocol: 'openai-responses', expected: ['INSTRUCTIONS'] },
    { protocol: 'anthropic-messages', expected: [blocks] },
    { protocol: 'google-generative-language', expected: [parts] },
    { protocol: 'openai-chat-completions', expected: [] },
    { protocol: 'aws-event-stream', expected: [] },
  ] as const)('reads only the system field of $protocol, unchanged', ({ protocol, expected }) => {
    const fields = requestSystemFields(protocol, body)
    expect(fields).toEqual(expected)
    for (const [index, field] of fields.entries())
      expect(field).toBe(expected[index])
  })

  it.each([
    { protocol: 'openai-responses', body: { input: [] } },
    { protocol: 'anthropic-messages', body: { messages: [] } },
    { protocol: 'google-generative-language', body: { contents: [] } },
    { protocol: 'google-generative-language', body: { systemInstruction: 'SYSTEM' } },
    { protocol: 'google-generative-language', body: { systemInstruction: {} } },
  ] as const)('returns no field for a $protocol body that states none: %j', ({ protocol, body }) => {
    expect(requestSystemFields(protocol, body)).toEqual([])
  })

  it.each([undefined, null, 'body', [], 0])('returns no field for a body that is not an object: %j', (value) => {
    expect(requestSystemFields('anthropic-messages', value)).toEqual([])
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
