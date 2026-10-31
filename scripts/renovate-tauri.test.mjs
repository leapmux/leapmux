import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { parseBunLock, parseCargoLock, parseCargoManifest } from './renovate-lockfile-versions.mjs'
import { assertTauriCompatibility, readTauriVersions, reconcileCargoRequirement, reconcileTauri, tauriAuthorizedRoots, tauriPairs, tauriVersions } from './renovate-tauri.mjs'
import { crateIndexResponse, isCrateIndexRead, scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)

describe('readTauriVersions', () => {
  it('identifies direct pairs in the real lockfiles while retaining nested vulnerability occurrences', () => {
    const versions = readTauriVersions(root)
    assertTauriCompatibility(versions)
    const lock = parseBunLock(readFileSync(resolve(root, 'frontend/bun.lock'), 'utf8'))
    expect(`@tauri-apps/api@${versions.npm['@tauri-apps/api']}`).toBe(lock.packages['@tauri-apps/api'][0])
    for (const [identity] of Object.values(lock.packages).filter(([identity]) => identity.startsWith('@tauri-apps/api@')))
      expect(versions.occurrences.npm['@tauri-apps/api']).toContain(identity.slice('@tauri-apps/api@'.length))
    expect(Object.keys(versions.npm).every(name => /^@tauri-apps\/[^/]+$/.test(name))).toBe(true)
  })
  it('pins every Tauri pair exactly in main, so lockfile maintenance never rewrites a manifest', () => {
    // reconcileTauri writes exact pairs whenever a resolver moves one side of a pair.
    // Exact pins in main leave bun and cargo nothing to move.
    const pairs = tauriPairs(readTauriVersions(root))
    expect(pairs.length).toBeGreaterThan(0)
    const manifest = JSON.parse(readFileSync(resolve(root, 'frontend/package.json'), 'utf8'))
    const cargo = parseCargoManifest(readFileSync(resolve(root, 'desktop/rust/Cargo.toml'), 'utf8'))
    for (const pair of pairs) {
      expect({
        ...manifest.dependencies,
        ...manifest.devDependencies,
      }[pair.npm], pair.npm).toBe(pair.npmVersion)
      const declaration = cargo.dependencies[pair.cargo]
      expect(typeof declaration === 'string'
        ? declaration
        : declaration?.version, pair.cargo).toBe(`=${pair.cargoVersion}`)
    }
  })
  it('reads the real lockfiles and manifest exactly as the Bun parsers did', () => {
    const bun = readFileSync(resolve(root, 'frontend/bun.lock'), 'utf8')
    const cargo = readFileSync(resolve(root, 'desktop/rust/Cargo.lock'), 'utf8')
    const manifest = readFileSync(resolve(root, 'desktop/rust/Cargo.toml'), 'utf8')
    expect(parseBunLock(bun)).toEqual(Bun.JSON5.parse(bun))
    expect(parseCargoLock(cargo)).toEqual(Bun.TOML.parse(cargo))
    expect(parseCargoManifest(manifest)).toEqual(Bun.TOML.parse(manifest))
  })
  it('keeps nested package identities out of direct pairs and preserves every locked version', () => {
    const versions = tauriVersions(JSON.stringify({
      packages: {
        '@tauri-apps/api': ['@tauri-apps/api@2.11.1'],
        '@tauri-apps/plugin-clipboard-manager/@tauri-apps/api': ['@tauri-apps/api@2.10.1'],
      },
    }), '[[package]]\nname = "tauri"\nversion = "2.11.3"\n', '[dependencies]\ntauri = "2"\n')
    expect(versions.npm).toEqual({ '@tauri-apps/api': '2.11.1' })
    expect(versions.occurrences.npm['@tauri-apps/api']).toEqual(['2.10.1', '2.11.1'])
  })
})

function tauriFixture({ core = false, proposedCore = '2.11.0' } = {}) {
  const fixture = scratch.create('tauri-companion')
  const trusted = resolve(fixture, 'trusted')
  const proposed = resolve(fixture, 'proposed')
  const lock = (version, coreVersion = '2.11.0') => JSON.stringify({
    packages: {
      ...(core
        ? {
            '@tauri-apps/api': [`@tauri-apps/api@${coreVersion.replace(/^[~^=]/, '')}`],
          }
        : {}),
      '@tauri-apps/plugin-opener': [`@tauri-apps/plugin-opener@${version.replace(/^[~^=]/, '')}`],
    },
  })
  for (const directory of [trusted, proposed]) {
    mkdirSync(resolve(directory, 'frontend'), { recursive: true })
    mkdirSync(resolve(directory, 'desktop/rust'), { recursive: true })
    mkdirSync(resolve(directory, '.tmp'))
    writeFileSync(resolve(directory, 'frontend/bun.lock'), lock('2.5.0'))
    writeFileSync(resolve(directory, 'frontend/package.json'), JSON.stringify({
      dependencies: {
        ...(core
          ? { '@tauri-apps/api': '^2.11.0' }
          : {}),
        '@tauri-apps/plugin-opener': '^2.5.0',
      },
    }))
    writeFileSync(resolve(directory, 'desktop/rust/Cargo.toml'), `[dependencies]\n${core ? 'tauri = "2"\n' : ''}tauri-plugin-opener = "2"\n`)
    writeFileSync(resolve(directory, 'desktop/rust/Cargo.lock'), `${core
      ? `[[package]]\nname = "tauri"\nversion = "${directory === trusted
        ? '2.11.0'
        : proposedCore}"\n`
      : ''}[[package]]\nname = "tauri-plugin-opener"\nversion = "${directory === trusted
      ? '2.5.0'
      : '2.6.0'}"\n`)
  }
  writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({ alerts: [] }))
  writeFileSync(resolve(trusted, 'renovate.json'), readFileSync(resolve(root, 'renovate.json')))
  return { proposed, trusted, lock }
}

describe('reconcileTauri', () => {
  it('does not modify an earlier pair when a later pair has no compatible target', async () => {
    const { proposed, trusted } = tauriFixture({ core: true, proposedCore: '2.12.0' })
    const files = [
      'frontend/package.json',
      'frontend/bun.lock',
      'desktop/rust/Cargo.toml',
      'desktop/rust/Cargo.lock',
    ]
    const before = files.map(file => readFileSync(resolve(proposed, file), 'utf8'))
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    const calls = []
    await expect(reconcileTauri({
      root: proposed,
      trusted,
      upgrades: [
        { depName: 'tauri', newVersion: '2.12.0' },
        { depName: 'tauri-plugin-opener', newVersion: '2.6.0' },
      ],
      request: async url => isCrateIndexRead(url)
        ? crateIndexResponse([
            {
              vers: url.includes('plugin-opener')
                ? '2.6.0'
                : '2.12.0',
              pubtime: publishedAt,
            },
          ])
        : Response.json(url.includes('/dependencies')
            ? { dependencies: [] }
            : {
                time: url.includes('plugin-opener')
                  ? {}
                  : { '2.12.0': publishedAt },
              }),
      run: (...args) => calls.push(args),
    })).rejects.toThrow('compatible')
    expect(files.map(file => readFileSync(resolve(proposed, file), 'utf8'))).toEqual(before)
    expect(calls).toEqual([])
  })
  it('starts registry reads for every changed pair before waiting for responses', async () => {
    const { proposed, trusted, lock } = tauriFixture({ core: true, proposedCore: '2.12.0' })
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    const pending = []
    let released = false
    const version = url => url.includes('plugin-opener')
      ? '2.6.0'
      : '2.12.0'
    const response = url => isCrateIndexRead(url)
      ? crateIndexResponse([
          {
            vers: version(url),
            pubtime: publishedAt,
          },
        ])
      : Response.json(url.includes('/dependencies')
          ? { dependencies: [] }
          : {
              time: { [version(url)]: publishedAt },
            })
    const running = reconcileTauri({
      root: proposed,
      trusted,
      request: url => released
        ? response(url)
        : new Promise(resolve => pending.push({ url, resolve })),
      run: (command) => {
        expect(command).toBe('bun')
        const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
        writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener'], manifest.dependencies['@tauri-apps/api']))
      },
    })
    await Promise.resolve()
    const simultaneous = pending.length
    released = true
    for (const request of pending) request.resolve(response(request.url))
    await running
    expect(simultaneous).toBe(4)
  })
  it('includes a previously unchanged core pair when a plugin requires a newer core', async () => {
    const { proposed, trusted, lock } = tauriFixture({ core: true })
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    const request = async url => isCrateIndexRead(url)
      ? crateIndexResponse((url.includes('plugin-opener')
          ? ['2.6.0', '2.7.0']
          : ['2.11.0', '2.12.0']).map(vers => ({ vers, pubtime: publishedAt })))
      : Response.json(url.includes('/dependencies')
          ? {
              dependencies: url.includes('plugin-opener')
                ? [
                    {
                      crate_id: 'tauri',
                      req: '2.12',
                      kind: 'normal',
                      optional: false,
                    },
                  ]
                : [],
            }
          : {
              time: Object.fromEntries((url.includes('plugin-opener')
                ? ['2.7.0']
                : ['2.11.0', '2.12.0']).map(version => [version, publishedAt])),
            })
    const calls = []
    await reconcileTauri({
      root: proposed,
      trusted,
      upgrades: [
        { depName: 'tauri-plugin-opener', newVersion: '2.6.0' },
      ],
      request,
      run: (command, args) => {
        calls.push(command)
        if (command === 'cargo') {
          expect(args).toEqual(['update', '--workspace'])
          expect(parseCargoManifest(readFileSync(resolve(proposed, 'desktop/rust/Cargo.toml'), 'utf8')).dependencies).toEqual({ 'tauri': '=2.12.0', 'tauri-plugin-opener': '=2.7.0' })
          writeFileSync(resolve(proposed, 'desktop/rust/Cargo.lock'), '[[package]]\nname = "tauri"\nversion = "2.12.0"\n[[package]]\nname = "tauri-plugin-opener"\nversion = "2.7.0"\n')
        }
        else {
          expect(command).toBe('bun')
          const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
          writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener'], manifest.dependencies['@tauri-apps/api']))
        }
      },
    })
    const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
    expect(manifest.dependencies['@tauri-apps/api']).toBe('2.12.0')
    expect(manifest.dependencies['@tauri-apps/plugin-opener']).toBe('2.7.0')
    expect(calls).toEqual(['cargo', 'bun'])
  })
  it('rejects a prepared security target that remains inside another affected range before writing', async () => {
    const { proposed, trusted } = tauriFixture()
    const vulnerability = {
      package: { name: 'tauri-plugin-opener', ecosystem: 'rust' },
      vulnerable_version_range: '>= 2.6.0, < 2.6.2',
      first_patched_version: { identifier: '2.6.2' },
    }
    writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({
      alerts: [
        {
          state: 'open',
          dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
          security_vulnerability: vulnerability,
        },
      ],
      securityTargets: { '@tauri-apps/plugin-opener': '2.6.3', 'tauri-plugin-opener': '2.6.0' },
      authorized: {},
    }))
    const before = readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8')
    await expect(reconcileTauri({
      root: proposed,
      trusted,
      security: true,
      request: async () => {
        throw new Error('Unexpected request.')
      },
      run: () => {
        throw new Error('Unexpected execution.')
      },
    })).rejects.toThrow('vulnerable')
    expect(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8')).toBe(before)
  })
  it('adds an exact JavaScript companion when Cargo moves to a new minor', async () => {
    const { proposed, trusted, lock } = tauriFixture()
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    await reconcileTauri({
      root: proposed,
      trusted,
      request: async url => isCrateIndexRead(url)
        ? crateIndexResponse([
            { vers: '2.5.0', pubtime: publishedAt },
            { vers: '2.6.0', pubtime: publishedAt },
          ])
        : Response.json(url.includes('/dependencies')
            ? { dependencies: [] }
            : {
                time: { '2.5.0': publishedAt, '2.6.3': publishedAt },
              }),
      run: (command, args) => {
        expect(command).toBe('bun')
        expect(args).toEqual(['install', '--ignore-scripts'])
        const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
        expect(manifest.dependencies['@tauri-apps/plugin-opener']).toBe('2.6.3')
        writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener']))
      },
    })
    const versions = tauriVersions(readFileSync(resolve(proposed, 'frontend/bun.lock'), 'utf8'), readFileSync(resolve(proposed, 'desktop/rust/Cargo.lock'), 'utf8'))
    expect(() => assertTauriCompatibility(versions)).not.toThrow()
  })
  it('does not downgrade an explicit proposal to an older compatible minor', async () => {
    const { proposed, trusted } = tauriFixture()
    const publishedAt = new Date(Date.now() - 4 * 86400000).toISOString()
    await expect(reconcileTauri({
      root: proposed,
      trusted,
      upgrades: [
        { depName: 'tauri-plugin-opener', newVersion: '2.6.0' },
      ],
      request: async url => isCrateIndexRead(url)
        ? crateIndexResponse([
            { vers: '2.5.0', pubtime: publishedAt },
            { vers: '2.6.0', pubtime: publishedAt },
          ])
        : Response.json(url.includes('/dependencies')
            ? { dependencies: [] }
            : {
                time: { '2.5.0': publishedAt },
              }),
      run: () => { throw new Error('Unexpected mutation.') },
    })).rejects.toThrow('No compatible Tauri graph')
  })
  it('rejects a malformed authorized map of the prepared file before any registry read or write', async () => {
    const { proposed, trusted } = tauriFixture()
    for (const authorized of [
      {
        crates: { 'tauri-plugin-opener@2.6.0': [] },
      },
      {
        'desktop/rust/Cargo.lock': { 'tauri-plugin-opener@2.6.0': 'tauri@2.11.0' },
      },
      [],
    ]) {
      writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({ alerts: [], securityTargets: {}, authorized }))
      await expect(reconcileTauri({
        root: proposed,
        trusted,
        security: true,
        request: async () => {
          throw new Error('Unexpected registry read.')
        },
        run: () => {
          throw new Error('Unexpected execution.')
        },
      }), JSON.stringify(authorized)).rejects.toThrow(/unexpected lockfile|must list package identities|must map each lockfile path/)
    }
  })
  it('requires the authorized fixes of the prepared file for security processing', async () => {
    const { proposed, trusted } = tauriFixture()
    await expect(reconcileTauri({
      root: proposed,
      trusted,
      security: true,
      request: async () => {
        throw new Error('Unexpected registry read.')
      },
      run: () => {
        throw new Error('Unexpected execution.')
      },
    })).rejects.toThrow('Security processing requires the authorized fixes of the prepared file.')
  })
  it('selects a young authorized fix on a security branch only', async () => {
    const day = 86400000
    const old = new Date(Date.now() - 4 * day).toISOString()
    // Old enough for a security fix (two hours), and too young for a routine update.
    const young = new Date(Date.now() - 3 * 3600000).toISOString()
    const attempt = (crates, security) => {
      const { proposed, trusted, lock } = tauriFixture()
      writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({
        alerts: [
          {
            state: 'open',
            dependency: { manifest_path: 'desktop/rust/Cargo.lock' },
            security_vulnerability: {
              package: { name: 'tauri-plugin-opener', ecosystem: 'rust' },
              vulnerable_version_range: '< 2.6.0',
              first_patched_version: { identifier: '2.6.0' },
            },
          },
        ],
        securityTargets: {},
        authorized: { 'desktop/rust/Cargo.lock': crates },
      }))
      const calls = []
      const running = reconcileTauri({
        root: proposed,
        trusted,
        security,
        upgrades: [
          { depName: 'tauri-plugin-opener', newVersion: '2.6.0', isVulnerabilityAlert: security },
        ],
        request: async url => isCrateIndexRead(url)
          ? crateIndexResponse([
              { vers: '2.5.0', pubtime: old },
              { vers: '2.6.0', pubtime: young },
            ])
          : Response.json(url.includes('/dependencies')
              ? { dependencies: [] }
              : {
                  time: { '2.5.0': old, '2.6.0': old },
                }),
        run: (command) => {
          calls.push(command)
          const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
          writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener']))
        },
      })
      return { proposed, calls, running }
    }
    const authorized = { 'tauri-plugin-opener@2.6.0': [] }
    const fixed = attempt(authorized, true)
    await fixed.running
    expect(fixed.calls).toEqual(['bun'])
    expect(JSON.parse(readFileSync(resolve(fixed.proposed, 'frontend/package.json'), 'utf8')).dependencies['@tauri-apps/plugin-opener']).toBe('2.6.0')
    expect(parseCargoManifest(readFileSync(resolve(fixed.proposed, 'desktop/rust/Cargo.toml'), 'utf8')).dependencies['tauri-plugin-opener']).toBe('=2.6.0')
    // Without the root, and on a routine branch even with it, the young crate waits.
    for (const [crates, security] of [
      [{}, true],
      [authorized, false],
    ]) {
      const rejected = attempt(crates, security)
      await expect(rejected.running).rejects.toThrow('No compatible Tauri graph')
      expect(rejected.calls).toEqual([])
    }
  })
  it('uses the prepared security pair without choosing a different release during execution', async () => {
    const { proposed, trusted, lock } = tauriFixture()
    writeFileSync(resolve(trusted, '.tmp/renovate-prepared.json'), JSON.stringify({
      alerts: [],
      securityTargets: { '@tauri-apps/plugin-opener': '2.6.3', 'tauri-plugin-opener': '2.6.0' },
      authorized: {},
    }))
    await reconcileTauri({
      root: proposed,
      trusted,
      security: true,
      request: async () => {
        throw new Error('Unexpected registry read.')
      },
      run: (command) => {
        expect(command).toBe('bun')
        const manifest = JSON.parse(readFileSync(resolve(proposed, 'frontend/package.json'), 'utf8'))
        expect(manifest.dependencies['@tauri-apps/plugin-opener']).toBe('2.6.3')
        writeFileSync(resolve(proposed, 'frontend/bun.lock'), lock(manifest.dependencies['@tauri-apps/plugin-opener']))
      },
    })
  })
})

describe('readTauriVersions', () => {
  it('loads both lockfiles and the direct Cargo requirement from one checkout', () => {
    const { trusted } = tauriFixture({ core: true })
    writeFileSync(resolve(trusted, 'desktop/rust/Cargo.lock'), '[[package]]\nname = "tauri"\nversion = "2.11.3"\n[[package]]\nname = "tauri"\nversion = "3.0.0"\n[[package]]\nname = "tauri-plugin-opener"\nversion = "2.5.0"\n')
    expect(readTauriVersions(trusted).cargo.tauri).toBe('2.11.3')
    expect(() => readTauriVersions(resolve(trusted, 'missing'))).toThrow()
  })
})

describe('tauriAuthorizedRoots', () => {
  const pairs = [
    {
      npm: '@tauri-apps/api',
      cargo: 'tauri',
      npmVersion: '2.11.0',
      cargoVersion: '2.11.0',
    },
    {
      npm: '@tauri-apps/plugin-opener',
      cargo: 'tauri-plugin-opener',
      npmVersion: '2.5.0',
      cargoVersion: '2.5.0',
    },
  ]
  it('keeps the roots of the Tauri packages, with their closures, from the lockfiles of both sides', () => {
    expect(tauriAuthorizedRoots({
      'frontend/bun.lock': {
        '@tauri-apps/api@2.11.1': ['@tauri-apps/cli@2.11.1'],
      },
      'desktop/rust/Cargo.lock': {
        'tauri-plugin-opener@2.5.1': ['tauri@2.11.1'],
      },
    }, pairs)).toEqual(new Map([
      [
        '@tauri-apps/api@2.11.1',
        new Set(['@tauri-apps/cli@2.11.1']),
      ],
      [
        'tauri-plugin-opener@2.5.1',
        new Set(['tauri@2.11.1']),
      ],
    ]))
  })
  it('drops the roots of other packages, and every root of another lockfile', () => {
    expect(tauriAuthorizedRoots({
      'frontend/bun.lock': { 'which@6.0.0': [] },
      'desktop/rust/Cargo.lock': {
        'which@6.0.0': ['tauri@2.11.1'],
      },
      // The Renovate tree is not a side of a Tauri pair, even for a Tauri package name.
      '.github/renovate/bun.lock': { '@tauri-apps/api@2.11.1': [] },
    }, pairs)).toEqual(new Map())
  })
  it('authorizes nothing without a prepared map', () => {
    expect(tauriAuthorizedRoots(undefined, pairs)).toEqual(new Map())
  })
})

describe('reconcileCargoRequirement', () => {
  it('preserves Cargo caret semantics for a bare version and comma-separated comparisons', () => {
    for (const requirement of ['2.11.0', '2.11', '>= 2.11, < 3']) {
      const source = `[dependencies]\ntauri = "${requirement}" # retain\n`
      expect(reconcileCargoRequirement(source, 'tauri', '2.12.0')).toBe(source)
    }
  })
  it('preserves compatible requirements and repairs an incompatible major without losing features or comments', () => {
    const source = '[dependencies]\ntauri = { version = "2", features = ["tray-icon"] } # retain\n'
    expect(reconcileCargoRequirement(source, 'tauri', '2.11.3')).toBe(source)
    expect(reconcileCargoRequirement(source, 'tauri', '3.1.0')).toBe(source.replace('version = "2"', 'version = "3.1.0"'))
    expect(reconcileCargoRequirement('[dependencies]\ntauri = "2"\n', 'tauri', '3.1.0')).toBe('[dependencies]\ntauri = "3.1.0"\n')
    expect(() => reconcileCargoRequirement('', 'tauri', '3.1.0')).toThrow('Missing')
  })
})

describe('assertTauriCompatibility', () => {
  it('identifies a direct patched version under a bare Cargo requirement', () => {
    const bun = '{ "packages": { "@tauri-apps/api": ["@tauri-apps/api@2.11.1"] } }'
    const cargo = '[[package]]\nname = "tauri"\nversion = "2.11.3"\n[[package]]\nname = "tauri"\nversion = "3.0.0"\n'
    expect(tauriVersions(bun, cargo, '[dependencies]\ntauri = "2.11.0"\n').cargo.tauri).toBe('2.11.3')
  })
  it('checks core and plugin pairs while permitting independent CLI and Rust-only plugin versions', () => {
    const npm = { '@tauri-apps/api': '2.11.1', '@tauri-apps/plugin-opener': '2.5.4', '@tauri-apps/cli': '2.13.0' }
    const cargo = {
      'tauri': '2.11.9',
      'tauri-plugin-opener': '2.5.2',
      'tauri-build': '2.9.0',
      'tauri-plugin-dialog': '2.8.0',
    }
    expect(() => assertTauriCompatibility({ npm, cargo })).not.toThrow()
    expect(() => assertTauriCompatibility({
      npm,
      cargo: { ...cargo, 'tauri-plugin-opener': '2.6.0' },
    })).toThrow('mismatch')
    expect(() => assertTauriCompatibility({
      npm: { ...npm, '@tauri-apps/api': '3.11.1' },
      cargo,
    })).toThrow('mismatch')
  })
  it('extracts actual resolved versions from both lockfile formats', () => {
    const result = tauriVersions('{ "packages": { "@tauri-apps/api": ["@tauri-apps/api@2.11.1", "", {}, "hash"], }, }', '[[package]]\nname = "tauri"\nversion = "2.11.3"\n')
    expect(result).toEqual({
      npm: { '@tauri-apps/api': '2.11.1' },
      cargo: { tauri: '2.11.3' },
      occurrences: {
        npm: {
          '@tauri-apps/api': ['2.11.1'],
        },
        cargo: {
          tauri: ['2.11.3'],
        },
      },
    })
  })
  it('uses the direct Cargo requirement when another major exists transitively', () => {
    const bun = '{ "packages": { "@tauri-apps/api": ["@tauri-apps/api@2.11.1"] } }'
    const cargo = '[[package]]\nname = "tauri"\nversion = "2.11.3"\n[[package]]\nname = "tauri"\nversion = "3.0.0"\n'
    expect(tauriVersions(bun, cargo, '[dependencies]\ntauri = "2"\n').cargo.tauri).toBe('2.11.3')
    expect(tauriVersions(bun, cargo, '[dependencies]\nruntime = { package = "tauri", version = "2" }\n').cargo.tauri).toBe('2.11.3')
    expect(() => tauriVersions(bun, cargo)).toThrow('direct locked Tauri version')
    expect(tauriVersions(bun, cargo, '[dependencies]\ntauri = "2"\n').occurrences.cargo.tauri).toEqual(['2.11.3', '3.0.0'])
    expect(() => tauriVersions(bun, '[[package]]\nname = "tauri"\nversion = "3.0.0"\n', '[dependencies]\ntauri = "2"\n')).toThrow('direct locked Tauri version')
  })
  it('rejects a direct Tauri package that bun.lock locks outside the npm registry or under another name', () => {
    const cargo = '[[package]]\nname = "tauri"\nversion = "2.11.3"\n'
    for (const identity of [
      '@tauri-apps/api@workspace:packages/api',
      '@tauri-apps/api@github:tauri-apps/tauri#abc123',
      'tauri-api-fork@2.11.1',
    ]) {
      const bun = JSON.stringify({
        packages: {
          '@tauri-apps/api': [identity],
        },
      })
      expect(() => tauriVersions(bun, cargo), identity).toThrow(`bun.lock must lock @tauri-apps/api at an npm registry version, not ${identity}.`)
    }
    // A nested copy of another package under a Tauri key path is not a direct pair.
    const nested = JSON.stringify({
      packages: {
        '@tauri-apps/api': ['@tauri-apps/api@2.11.1'],
        'x/@tauri-apps/api': ['@tauri-apps/api@2.10.0'],
      },
    })
    expect(tauriVersions(nested, cargo).occurrences.npm['@tauri-apps/api']).toEqual(['2.10.0', '2.11.1'])
  })
})
