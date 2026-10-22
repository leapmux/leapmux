import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'bun:test'
import { emitGoProviderProtocol, emitTsProviderProtocol, PROVIDER_PROTOCOLS } from './generate-contracts.mjs'

describe('junie permission contract', () => {
  it('generates the Brave option ID for both language consumers', () => {
    const spec = PROVIDER_PROTOCOLS.find(spec => spec.name === 'junie-protocol')
    const contract = JSON.parse(readFileSync(new URL('../contracts/junie-protocol.json', import.meta.url), 'utf8'))

    expect(emitGoProviderProtocol(spec, contract)).toMatch(/JunieConfigBraveMode\s+=\s+"brave_mode"/u)
    const typescript = emitTsProviderProtocol(spec, contract)
    expect(typescript).toContain('export const JUNIE_CONFIG')
    expect(typescript).toContain('BraveMode: "brave_mode"')
  })
})
