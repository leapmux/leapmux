import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { assertTauriCompatibility, chooseTauriPair, fixedTauriVersion, reconcileCargoRequirement, reconcileTauri, tauriReleaseAgeMs, tauriVersions } from './renovate-tauri.mjs'

const now = Date.parse('2026-10-26T00:00:00Z')
const release = (version, publishedAt = '2026-10-20T00:00:00Z') => ({ version, publishedAt })
const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

function tauriFixture() {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const fixture = mkdtempSync(resolve(root, '.tmp/tauri-companion-'))
  fixtures.push(fixture)
  const trusted = resolve(fixture, 'trusted')
  const proposed = resolve(fixture, 'proposed')
  const lock = version => JSON.stringify({ packages: { '@tauri-apps/plugin-opener': [`@tauri-apps/plugin-opener@${version}`] } })
  for (const directory of [trusted, proposed]) {
    mkdirSync(resolve(directory, 'frontend'), { recursive: true })
    mkdirSync(resolve(directory, 'desktop/rust'), { recursive: true })
    mkdirSync(resolve(directory, '.tmp'))
    writeFileSync(resolve(directory, 'frontend/bun.lock'), lock('2.5.0'))
    writeFileSync(resolve(directory, 'frontend/package.json'), JSON.stringify({ dependencies: { '@tauri-apps/plugin-opener': '^2.5.0' } }))
    writeFileSync(resolve(directory, 'desktop/rust/Cargo.toml'), '[dependencies]\ntauri-plugin-opener = "2"\n')
    writeFileSync(resolve(directory, 'desktop/rust/Cargo.lock'), `[[package]]\nname = "tauri-plugin-opener"\nversion = "${directory === trusted ? '2.5.0' : '2.6.0'}"\n`)
  }
  writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({ alerts: [] }))
  writeFileSync(resolve(trusted, 'renovate.json'), readFileSync(resolve(root, 'renovate.json')))
  return { proposed, trusted, lock }
}

describe('tauriReleaseAgeMs', () => {
  it('derives routine and security ages from the trusted configuration', () => {
    const config = { minimumReleaseAge: '4 days', minimumReleaseAgeBuffer: null, vulnerabilityAlerts: { minimumReleaseAge: '3 days' } }
    expect(tauriReleaseAgeMs(config)).toBe(4 * 86400000)
    expect(tauriReleaseAgeMs(config, true)).toBe(3 * 86400000)
    for (const minimumReleaseAge of ['0 days', '-1 days', '', '999999999999999 days'])
      expect(() => tauriReleaseAgeMs({ ...config, minimumReleaseAge })).toThrow()
    expect(() => tauriReleaseAgeMs({ ...config, minimumReleaseAgeBuffer: '30 minutes' })).toThrow()
  })
})

describe('fixedTauriVersion', () => {
  it('preserves valid fixed versions and rejects malformed alert metadata', () => {
    expect(fixedTauriVersion({})).toBeNull()
    expect(fixedTauriVersion({ security_vulnerability: { first_patched_version: { identifier: '3.0.0' } } })).toBe('3.0.0')
    for (const identifier of ['', 'not-a-version', 2])
      expect(() => fixedTauriVersion({ security_vulnerability: { first_patched_version: { identifier } } })).toThrow()
  })
})

describe('reconcileTauri', () => {
  it('adds an exact JavaScript companion when Cargo moves to a new minor', async () => {
    const { proposed, trusted, lock } = tauriFixture()
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    await reconcileTauri({ root: proposed, trusted, request: async url => Response.json(url.includes('registry.npmjs.org')
      ? { time: { '2.5.0': publishedAt, '2.6.3': publishedAt } }
      : { versions: [{ num: '2.5.0', created_at: publishedAt }, { num: '2.6.0', created_at: publishedAt }] }), run: (command, args) => {
      expect(command).toBe('bun')
      expect(args).toEqual(['install', '--ignore-scripts'])
      const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
      expect(manifest.dependencies['@tauri-apps/plugin-opener']).toBe('2.6.3')
      writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener']))
    } })
    const versions = tauriVersions(readFileSync(resolve(proposed, 'frontend/bun.lock'), 'utf8'), readFileSync(resolve(proposed, 'desktop/rust/Cargo.lock'), 'utf8'))
    expect(() => assertTauriCompatibility(versions)).not.toThrow()
  })
  it('does not downgrade an explicit proposal to an older compatible minor', async () => {
    const { proposed, trusted } = tauriFixture()
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    await expect(reconcileTauri({ root: proposed, trusted, upgrades: [{ depName: 'tauri-plugin-opener', newVersion: '2.6.0' }], request: async url => Response.json(url.includes('registry.npmjs.org')
      ? { time: { '2.5.0': publishedAt } }
      : { versions: [{ num: '2.5.0', created_at: publishedAt }, { num: '2.6.0', created_at: publishedAt }] }), run: () => { throw new Error('Unexpected mutation.') } })).rejects.toThrow('No compatible Tauri pair')
  })
  it('uses the prepared security pair without choosing a different release during execution', async () => {
    const { proposed, trusted, lock } = tauriFixture()
    writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({ alerts: [], securityTargets: { '@tauri-apps/plugin-opener': '2.6.3', 'tauri-plugin-opener': '2.6.0' } }))
    await reconcileTauri({ root: proposed, trusted, security: true, request: async () => {
      throw new Error('Unexpected registry read.')
    }, run: (command) => {
      expect(command).toBe('bun')
      const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
      expect(manifest.dependencies['@tauri-apps/plugin-opener']).toBe('2.6.3')
      writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener']))
    } })
  })
})

describe('reconcileCargoRequirement', () => {
  it('preserves compatible requirements and repairs an incompatible major without losing features or comments', () => {
    const source = '[dependencies]\ntauri = { version = "2", features = ["tray-icon"] } # retain\n'
    expect(reconcileCargoRequirement(source, 'tauri', '2.11.3')).toBe(source)
    expect(reconcileCargoRequirement(source, 'tauri', '3.1.0')).toBe(source.replace('version = "2"', 'version = "3.1.0"'))
    expect(reconcileCargoRequirement('[dependencies]\ntauri = "2"\n', 'tauri', '3.1.0')).toBe('[dependencies]\ntauri = "3.1.0"\n')
    expect(() => reconcileCargoRequirement('', 'tauri', '3.1.0')).toThrow('Missing')
  })
})

describe('chooseTauriPair', () => {
  it('keeps routine companions within the selected major while permitting approved or security majors', () => {
    const npm = [release('2.11.2'), release('3.0.0')]
    const cargo = [release('2.11.4'), release('3.0.1')]
    const options = { minimumAgeMs: 3 * 86400000, npmMinimum: '2.10.0', cargoMinimum: '2.10.0', now }
    expect(chooseTauriPair(npm, cargo, options)).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    expect(chooseTauriPair(npm, cargo, { ...options, maximumMajor: 3 })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
    expect(chooseTauriPair(npm, cargo, { ...options, npmMinimum: '3.0.0', security: true })).toEqual({ npm: '3.0.0', cargo: '3.0.1' })
  })
  for (const kind of ['routine', 'security', 'maintenance']) {
    it(`keeps ${kind} updates compatible with independent patch versions`, () => {
      const result = chooseTauriPair([release('2.10.1'), release('2.11.2'), release('2.12.0')], [release('2.10.9'), release('2.11.4')], {
        minimumAgeMs: 3 * 86400000,
        npmMinimum: '2.10.0',
        cargoMinimum: '2.10.0',
        security: kind === 'security',
        now,
      })
      expect(result).toEqual(kind === 'security' ? { npm: '2.10.1', cargo: '2.10.9' } : { npm: '2.11.2', cargo: '2.11.4' })
    })
  }
  it('requires fixed versions and excludes pending, missing-timestamp, prerelease, and yanked candidates', () => {
    const npm = [release('2.10.1'), release('2.11.2'), release('2.12.0', '2026-10-25T00:00:00Z'), release('3.0.0-beta.1')]
    const cargo = [release('2.10.9'), release('2.11.4'), release('2.12.0'), { ...release('3.0.0'), yanked: true }]
    expect(chooseTauriPair(npm, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '2.11.0', cargoMinimum: '2.10.0', security: true, now })).toEqual({ npm: '2.11.2', cargo: '2.11.4' })
    for (const candidates of [[], [release('2.11.2', null)], [release('2.11.2', '2026-10-25T00:00:00Z')]])
      expect(() => chooseTauriPair(candidates, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '2.11.0', cargoMinimum: '2.10.0', now })).toThrow()
    expect(() => chooseTauriPair(npm, cargo, { minimumAgeMs: 3 * 86400000, npmMinimum: '4.0.0', cargoMinimum: '2.10.0', now })).toThrow()
  })
})

describe('assertTauriCompatibility', () => {
  it('checks core and plugin pairs while permitting independent CLI and Rust-only plugin versions', () => {
    const npm = { '@tauri-apps/api': '2.11.1', '@tauri-apps/plugin-opener': '2.5.4', '@tauri-apps/cli': '2.13.0' }
    const cargo = { 'tauri': '2.11.9', 'tauri-plugin-opener': '2.5.2', 'tauri-build': '2.9.0', 'tauri-plugin-dialog': '2.8.0' }
    expect(() => assertTauriCompatibility({ npm, cargo })).not.toThrow()
    expect(() => assertTauriCompatibility({ npm, cargo: { ...cargo, 'tauri-plugin-opener': '2.6.0' } })).toThrow('mismatch')
    expect(() => assertTauriCompatibility({ npm: { ...npm, '@tauri-apps/api': '3.11.1' }, cargo })).toThrow('mismatch')
  })
  it('extracts actual resolved versions from both lockfile formats', () => {
    const result = tauriVersions('{ "packages": { "@tauri-apps/api": ["@tauri-apps/api@2.11.1", "", {}, "hash"], }, }', '[[package]]\nname = "tauri"\nversion = "2.11.3"\n')
    expect(result).toEqual({ npm: { '@tauri-apps/api': '2.11.1' }, cargo: { tauri: '2.11.3' } })
  })
  it('uses the direct Cargo requirement when another major exists transitively', () => {
    const bun = '{ "packages": { "@tauri-apps/api": ["@tauri-apps/api@2.11.1"] } }'
    const cargo = '[[package]]\nname = "tauri"\nversion = "2.11.3"\n[[package]]\nname = "tauri"\nversion = "3.0.0"\n'
    expect(tauriVersions(bun, cargo, '[dependencies]\ntauri = "2"\n').cargo.tauri).toBe('2.11.3')
    expect(tauriVersions(bun, cargo, '[dependencies]\nruntime = { package = "tauri", version = "2" }\n').cargo.tauri).toBe('2.11.3')
    expect(() => tauriVersions(bun, cargo)).toThrow('direct locked Tauri version')
  })
})
