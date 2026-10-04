import { describe, expect, it } from 'vitest'
import { expectNativeCodeExecutionAbsent, nativeCodeExecutionSchema, validateNativeScriptCases } from './nativeCodeExecution'

describe('nativeCodeExecutionSchema', () => {
  const schema = { type: 'object', properties: { source: { type: 'string' }, options: { type: 'object' } } }
  const tool = { name: 'native_runner', parameters: schema }
  const request = (tools: unknown) => ({ protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', body: { tools } })

  it('reads exact direct and nested native schemas without changing them', () => {
    expect(nativeCodeExecutionSchema(request([{ function: tool }]), 'native_runner', { source: 'string', options: 'object' })).toBe(schema)
    expect(nativeCodeExecutionSchema(request([{ name: tool.name, input_schema: schema }]), 'native_runner', { source: 'string' })).toBe(schema)
  })

  it('reads the exact Google parametersJsonSchema from function declarations', () => {
    const body = { tools: [{ functionDeclarations: [{ name: tool.name, parametersJsonSchema: schema }] }] }
    expect(nativeCodeExecutionSchema({ protocol: 'google-generative-language', path: '/google', body }, tool.name, { source: 'string' })).toBe(schema)
  })

  it.each([undefined, [], [{ function: { name: 'other', parameters: schema } }], [{ function: tool }, { function: tool }]])('rejects a missing or ambiguous actual descriptor: %j', (tools) => {
    expect(() => nativeCodeExecutionSchema(request(tools), 'native_runner', { source: 'string' })).toThrow()
  })

  it.each([null, {}, { type: 'array', properties: {} }, { type: 'object', properties: {} }, { type: 'object', properties: { source: { type: 'number' } } }])('rejects an absent or changed source schema: %j', (parameters) => {
    expect(() => nativeCodeExecutionSchema(request([{ function: { name: tool.name, parameters } }]), 'native_runner', { source: 'string' })).toThrow()
  })

  it('rejects a proof without a tool or argument fields', () => {
    expect(() => nativeCodeExecutionSchema(request([{ function: tool }]), '', { source: 'string' })).toThrow('tool and its argument fields')
    expect(() => nativeCodeExecutionSchema(request([{ function: tool }]), tool.name, {})).toThrow('tool and its argument fields')
  })
})

describe('validateNativeScriptCases', () => {
  const output = { label: 'output', source: 'text(40 + 2)', expected: '42', failed: false }
  const failure = { label: 'failure', source: 'throw new Error(String(70 + 7))', expected: '77', failed: true }

  it('accepts actual computed output and a computed failure', () => {
    expect(() => validateNativeScriptCases([output, failure])).not.toThrow()
  })

  it.each([{ cases: [] }, { cases: [output] }, { cases: [output, output] }, { cases: [failure, failure] }])('requires both native outcomes: %j', ({ cases }) => {
    expect(() => validateNativeScriptCases(cases)).toThrow('output and failure cases')
  })

  it.each([
    { ...output, label: '' },
    { ...output, source: '' },
    { ...output, expected: '' },
    { ...output, source: 'text("42")' },
  ])('refuses a missing or predetermined proof: %j', (invalid) => {
    expect(() => validateNativeScriptCases([invalid, failure])).toThrow('require script execution')
  })
})

describe('expectNativeCodeExecutionAbsent', () => {
  it('checks an actual nonempty native catalog', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [{ function: { name: 'native_read' } }] } }, ['exec', 'codemode'])).not.toThrow()
  })

  it('rejects a native executor that the catalog actually offers', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'anthropic-messages', path: '/v1/messages', body: { tools: [{ name: 'exec' }] } }, ['exec'])).toThrow()
  })

  it('rejects an empty catalog and absent executor definitions', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [] } }, ['exec'])).toThrow('nonempty tool catalog')
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {} }, [])).toThrow('audited executor names')
  })
})
