import { describe, expect, it } from 'bun:test'
import { advisoryEcosystem, ecosystemOfAdvisory, ECOSYSTEMS } from './renovate-ecosystems.mjs'
import { DEPENDENCY_INPUT_FILES } from './renovate-inputs.mjs'

describe('ECOSYSTEMS', () => {
  it('gives each ecosystem a distinct advisory name and a kind that Renovate manages', () => {
    const entries = Object.values(ECOSYSTEMS)
    expect(new Set(entries.map(entry => entry.advisory)).size).toBe(entries.length)
    expect(new Set(entries.map(entry => entry.kind)).size).toBe(entries.length)
    for (const { kind } of entries)
      expect(DEPENDENCY_INPUT_FILES, kind).toHaveProperty(kind)
  })
  it('names the manifest that each managed input file of a kind holds', () => {
    for (const { kind, manifest } of Object.values(ECOSYSTEMS)) {
      for (const path of DEPENDENCY_INPUT_FILES[kind])
        expect(path.split('/').at(-1), path).toBe(manifest)
    }
  })
  it('cannot change at run time', () => {
    expect(Object.isFrozen(ECOSYSTEMS)).toBe(true)
    expect(Object.values(ECOSYSTEMS).every(Object.isFrozen)).toBe(true)
  })
})

describe('advisoryEcosystem', () => {
  it('maps each ecosystem to its GitHub name, and rejects any other name', () => {
    expect(['npm', 'crates', 'go'].map(advisoryEcosystem)).toEqual(['npm', 'rust', 'go'])
    for (const name of ['pip', 'rust', 'toString', undefined])
      expect(() => advisoryEcosystem(name), String(name)).toThrow('Unknown advisory ecosystem')
  })
})

describe('ecosystemOfAdvisory', () => {
  it('inverts advisoryEcosystem, and returns null for an unmanaged GitHub ecosystem', () => {
    for (const ecosystem of Object.keys(ECOSYSTEMS))
      expect(ecosystemOfAdvisory(advisoryEcosystem(ecosystem))).toBe(ecosystem)
    for (const advisory of [
      'actions',
      'pip',
      'crates',
      undefined,
      null,
    ])
      expect(ecosystemOfAdvisory(advisory), String(advisory)).toBeNull()
  })
})
