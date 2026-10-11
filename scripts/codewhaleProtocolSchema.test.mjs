import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { buildAjv } from './validate-json.mjs'

const root = resolve(import.meta.dirname, '..')
const schema = JSON.parse(readFileSync(resolve(root, 'contracts/codewhale-protocol.schema.json'), 'utf8'))
const contract = JSON.parse(readFileSync(resolve(root, 'contracts/codewhale-protocol.json'), 'utf8'))
const validate = buildAjv().compile(schema)

function mediaContract() {
  return structuredClone(contract)
}

describe('codewhale-protocol.schema.json', () => {
  it('accepts the native image fields and positive safe numeric limits', () => {
    expect(validate(mediaContract())).toBe(true)
  })

  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, '8192', null])('rejects an invalid native image limit: %j', (value) => {
    const data = mediaContract()
    data.mediaRules.MaxDimension = value
    expect(validate(data)).toBe(false)
  })

  it.each(['mediaFields', 'supplementFields', 'mediaTypes', 'mediaRules'])('rejects a missing native image table: %s', (key) => {
    const data = mediaContract()
    delete data[key]
    expect(validate(data)).toBe(false)
  })

  it('rejects empty native image tables and invalid dispatch field values', () => {
    expect(validate({ ...mediaContract(), mediaFields: {} })).toBe(false)
    expect(validate({ ...mediaContract(), supplementFields: {} })).toBe(false)
    expect(validate({ ...mediaContract(), mediaRules: {} })).toBe(false)
    expect(validate({ ...mediaContract(), mediaFields: { Version: 1 } })).toBe(false)
    expect(validate({ ...mediaContract(), supplementFields: { OutputFiles: '' } })).toBe(false)
  })
})
