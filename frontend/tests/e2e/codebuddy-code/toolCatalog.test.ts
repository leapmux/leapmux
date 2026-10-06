import { describe, expect, it } from 'vitest'
import { codebuddyReplSchema, parseCodebuddyReplDiscovery } from './toolCatalog'

const input = {
  type: 'object',
  properties: { code: { type: 'string' }, timeout: { type: 'number' }, description: { type: 'string' } },
  required: ['code'],
  additionalProperties: false,
}

function receipt(schema: unknown = input, name = 'REPL', count = 1): string {
  return `Found ${count} tool(s). Use DeferExecuteTool to invoke them.\n\n## ${name}\nExecute JavaScript code through REPL.\n\nParameters:\n\`\`\`json\n${JSON.stringify(schema, null, 2)}\n\`\`\``
}

describe('codebuddyReplSchema', () => {
  it('validates the actual direct PTC schema and keeps native optional fields', () => {
    expect(codebuddyReplSchema(input)).toEqual(input)
    expect(codebuddyReplSchema({ type: 'object', properties: { code: { type: 'string' } }, required: ['code'] })).toMatchObject({ required: ['code'] })
  })

  it('rejects an optional code field and invalid direct PTC parameter types', () => {
    expect(() => codebuddyReplSchema({ ...input, required: [] })).toThrow('code schema')
    expect(() => codebuddyReplSchema({ ...input, properties: { code: { type: 'number' } } })).toThrow('code schema')
  })
})

describe('parseCodebuddyReplDiscovery', () => {
  it('reads one exact native REPL heading and its required string code schema', () => {
    expect(parseCodebuddyReplDiscovery(receipt())).toEqual(input)
  })

  it('rejects a keyword fallback whose description mentions REPL and code', () => {
    const fallback = receipt({ type: 'object', properties: { cron: { type: 'string' } }, required: ['cron'] }, 'CronCreate')
    expect(fallback).toContain('REPL')
    expect(fallback).toContain('code')
    expect(() => parseCodebuddyReplDiscovery(fallback)).toThrow('exactly the REPL tool')
  })

  it.each(['REPL_extra', 'OtherREPL', 'CronCreate'])('rejects a different exact native identity: %s', (name) => {
    expect(() => parseCodebuddyReplDiscovery(receipt(input, name))).toThrow('exactly')
  })

  it.each([0, 2, -1, 1000])('rejects a native result count that differs from one: %s', (count) => {
    expect(() => parseCodebuddyReplDiscovery(receipt(input, 'REPL', count))).toThrow('exactly')
  })

  it.each([
    null,
    [],
    {},
    { ...input, required: [] },
    { ...input, properties: { code: { type: 'number' } } },
    { ...input, properties: { code: { type: 'string' }, timeout: { type: 'string' } } },
    { ...input, properties: { code: { type: 'string' }, description: { type: 'object' } } },
  ])('rejects an invalid native code schema: %j', (schema) => {
    expect(() => parseCodebuddyReplDiscovery(receipt(schema))).toThrow('code schema')
  })

  it('rejects an absent schema, duplicate schemas, invalid JSON, and incomplete result bytes', () => {
    expect(() => parseCodebuddyReplDiscovery('Found 1 tool(s). Use DeferExecuteTool to invoke them.\n\n## REPL\nNo parameters.')).toThrow('unique')
    expect(() => parseCodebuddyReplDiscovery(`${receipt()}\n\nParameters:\n\`\`\`json\n{}\n\`\`\``)).toThrow('unique')
    expect(() => parseCodebuddyReplDiscovery(receipt().replace('"object"', 'broken'))).toThrow(SyntaxError)
    expect(() => parseCodebuddyReplDiscovery(receipt().slice(0, -1))).toThrow('unique')
  })
})
