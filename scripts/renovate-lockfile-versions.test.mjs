import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'bun:test'
import { bunIdentity, cargoLine, cargoReplacementDowngrades, DEPENDENCY_LOCKFILES, ecosystemVersions, goModJson, introducedVersions, lockedVersions, lockfileInput } from './renovate-lockfile-versions.mjs'

const root = resolve(import.meta.dirname, '..')

function bunLock(identities) {
  // bun.lock is JSON5 with trailing commas, as bun writes it.
  return `{
  "lockfileVersion": 1,
  "workspaces": { "": { "name": "fixture" } },
  "packages": {
${identities.map(([key, identity]) => `    ${JSON.stringify(key)}: [${JSON.stringify(identity)}, "", {}, "sha512-fixture"],`).join('\n')}
  },
}
`
}

function cargoLock(packages) {
  return `version = 4\n\n${packages.map(([name, version, source = 'registry+https://github.com/rust-lang/crates.io-index']) => `[[package]]\nname = "${name}"\nversion = "${version}"\n${source === null
    ? ''
    : `source = "${source}"\n`}`).join('\n')}`
}

function goJson({ require = [], replace = [] }) {
  return JSON.stringify({
    Module: { Path: 'example.com/app' },
    Require: require.map(([Path, Version]) => ({ Path, Version })),
    Replace: replace.map(([Old, New, Version]) => ({
      Old: { Path: Old },
      New: Version
        ? { Path: New, Version }
        : { Path: New },
    })),
  })
}

function asObject(versions) {
  return Object.fromEntries([...versions].map(([name, set]) => [
    name,
    [...set].sort(),
  ]))
}

describe('bunIdentity', () => {
  it('splits scoped and unscoped registry identities', () => {
    expect(bunIdentity('@scope/pkg@1.2.3')).toEqual({ name: '@scope/pkg', version: '1.2.3' })
    expect(bunIdentity('pkg@1.2.3-beta.1')).toEqual({ name: 'pkg', version: '1.2.3-beta.1' })
  })
  it('returns null for every source that is not a registry version', () => {
    for (const identity of [
      'app@workspace:.',
      'local@link:../local',
      'file-dep@file:../file-dep',
      'git-dep@github:owner/repo#0123456789abcdef',
      'git-plus@git+https://example.com/repo.git#v1.0.0',
      'tarball@https://example.com/tarball-1.0.0.tgz',
      'no-version',
      '@scope/no-version',
    ])
      expect(bunIdentity(identity), identity).toBeNull()
  })
})

describe('lockedVersions', () => {
  it('reads only registry versions from bun.lock, keyed by the identity rather than the entry key', () => {
    const versions = lockedVersions('bun', bunLock([
      ['fixture', 'fixture@workspace:.'],
      ['@scope/pkg', '@scope/pkg@1.2.3'],
      ['other/@scope/pkg', '@scope/pkg@2.0.0'],
      ['alias', 'real-package@4.5.6'],
      ['git-dep', 'git-dep@github:owner/repo#0123456789abcdef'],
      ['tarball', 'tarball@https://example.com/tarball-1.0.0.tgz'],
    ]))
    expect(asObject(versions)).toEqual({
      '@scope/pkg': ['1.2.3', '2.0.0'],
      'real-package': ['4.5.6'],
    })
  })
  it('reads only registry crates from Cargo.lock', () => {
    const versions = lockedVersions('cargo', cargoLock([
      ['app', '0.1.0', null],
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
      ['git-crate', '0.4.0', 'git+https://github.com/example/git-crate?rev=abc#abc'],
      ['path-crate', '0.2.0', null],
    ]))
    expect(asObject(versions)).toEqual({
      bitflags: ['1.3.2', '2.13.1'],
    })
  })
  it('reads Go requirements and versioned replacement targets, but not local replacements', () => {
    const versions = lockedVersions('gomod', goJson({
      require: [
        ['example.com/direct', 'v1.2.3'],
        ['example.com/pseudo', 'v0.0.0-20260615104152-545b12e0e8c7'],
        ['google.golang.org/genproto', 'v0.0.0-20250101000000-aaaaaaaaaaaa'],
      ],
      replace: [
        ['google.golang.org/genproto', 'google.golang.org/genproto', 'v0.0.0-20250202000000-bbbbbbbbbbbb'],
        ['github.com/leapmux/leapmux', '../backend'],
      ],
    }))
    expect(asObject(versions)).toEqual({
      'example.com/direct': ['v1.2.3'],
      'example.com/pseudo': ['v0.0.0-20260615104152-545b12e0e8c7'],
      'google.golang.org/genproto': ['v0.0.0-20250101000000-aaaaaaaaaaaa', 'v0.0.0-20250202000000-bbbbbbbbbbbb'],
    })
  })
  it('rejects an unknown kind and a malformed entry', () => {
    expect(() => lockedVersions('pip', '')).toThrow('Unknown lockfile kind')
    expect(() => lockedVersions('bun', '{ "packages": { "x": "x@1.0.0" } }')).toThrow('must be an array')
    expect(() => lockedVersions('gomod', JSON.stringify({
      Require: [
        { Path: 'example.com/x' },
      ],
    }))).toThrow('Invalid gomod lockfile entry')
  })
  it('reads an empty lockfile as no versions', () => {
    expect(lockedVersions('bun', '{ "packages": {} }').size).toBe(0)
    expect(lockedVersions('cargo', 'version = 4\n').size).toBe(0)
    expect(lockedVersions('gomod', '{}').size).toBe(0)
  })
})

describe('introducedVersions', () => {
  it('reports only the versions that the head adds, for each kind', () => {
    expect(introducedVersions('bun', bunLock([
      ['a', 'a@1.0.0'],
      ['b', 'b@1.0.0'],
    ]), bunLock([
      ['a', 'a@1.1.0'],
      ['b', 'b@1.0.0'],
      ['@s/c', '@s/c@2.0.0'],
    ]))).toEqual(['@s/c@2.0.0', 'a@1.1.0'])
    expect(introducedVersions('cargo', cargoLock([
      ['foo', '1.4.1'],
    ]), cargoLock([
      ['foo', '1.4.1'],
      ['foo', '2.0.0'],
    ]))).toEqual(['foo@2.0.0'])
    expect(introducedVersions('gomod', goJson({
      require: [
        ['example.com/lib', 'v1.9.0'],
      ],
    }), goJson({
      require: [
        ['example.com/lib/v2', 'v2.0.0'],
      ],
    }))).toEqual(['example.com/lib/v2@v2.0.0'])
  })
  it('reports nothing for an unchanged lockfile that holds two lines of one crate', () => {
    const lock = cargoLock([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
    ])
    expect(introducedVersions('cargo', lock, lock)).toEqual([])
  })
  it('treats an absent base as locking nothing', () => {
    expect(introducedVersions('cargo', null, cargoLock([
      ['foo', '1.0.0'],
    ]))).toEqual(['foo@1.0.0'])
    expect(introducedVersions('bun', undefined, bunLock([]))).toEqual([])
  })
})

describe('cargoLine', () => {
  it('groups versions by Cargo compatibility', () => {
    expect(cargoLine('0.0.1')).not.toBe(cargoLine('0.0.2'))
    expect(cargoLine('0.3.1')).toBe(cargoLine('0.3.9'))
    expect(cargoLine('1.2')).toBe(cargoLine('1.9'))
    expect(cargoLine('1.2.0')).not.toBe(cargoLine('2.0.0'))
    expect([
      cargoLine('0.0.4'),
      cargoLine('0.3.9'),
      cargoLine('13.2.1-beta.1'),
    ]).toEqual(['0.0.4', '0.3', '13'])
  })
  it('rejects a version that is not numeric', () => {
    for (const version of ['', 'v1.0.0', 'x.y.z', undefined])
      expect(() => cargoLine(version), String(version)).toThrow('Invalid Cargo version')
  })
})

describe('cargoReplacementDowngrades', () => {
  it('reports a base version replaced by a lower version of the same line', () => {
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '1.4.2'],
    ]), cargoLock([
      ['foo', '1.4.1'],
    ]))).toEqual([
      { name: 'foo', from: '1.4.2', to: '1.4.1' },
    ])
  })
  it('compares against the highest removed version of the line', () => {
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '1.3.0'],
      ['foo', '1.5.0'],
    ]), cargoLock([
      ['foo', '1.4.0'],
    ]))).toEqual([
      { name: 'foo', from: '1.5.0', to: '1.4.0' },
    ])
  })
  it('accepts an addition beside a kept version and an upgrade of the lower of two versions', () => {
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '1.4.2'],
    ]), cargoLock([
      ['foo', '1.4.1'],
      ['foo', '1.4.2'],
    ]))).toEqual([])
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '1.2.0'],
      ['foo', '1.4.2'],
    ]), cargoLock([
      ['foo', '1.3.0'],
      ['foo', '1.4.2'],
    ]))).toEqual([])
  })
  it('never compares independent lines or unchanged duplicates', () => {
    const lock = cargoLock([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
    ])
    expect(cargoReplacementDowngrades(lock, lock)).toEqual([])
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '2.0.0'],
      ['foo', '1.4.2'],
    ]), cargoLock([
      ['foo', '2.0.0'],
      ['foo', '1.4.3'],
      ['foo', '0.9.0'],
    ]))).toEqual([])
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '0.3.5'],
    ]), cargoLock([
      ['foo', '0.2.9'],
    ]))).toEqual([])
  })
  it('reports no downgrade without a base lockfile', () => {
    expect(cargoReplacementDowngrades(null, cargoLock([
      ['foo', '1.4.1'],
    ]))).toEqual([])
  })
  it('reports no downgrade for a removal without a replacement in the line', () => {
    expect(cargoReplacementDowngrades(cargoLock([
      ['foo', '1.4.2'],
      ['bar', '1.0.0'],
    ]), cargoLock([
      ['bar', '1.0.0'],
    ]))).toEqual([])
  })
})

describe('goModJson', () => {
  it('parses go.mod text with the Go toolchain', () => {
    const parsed = JSON.parse(goModJson('module example.com/app\n\ngo 1.25\n\nrequire example.com/lib v1.2.3\n\nreplace example.com/old => example.com/new v0.1.0\n'))
    expect(parsed.Require).toEqual([
      { Path: 'example.com/lib', Version: 'v1.2.3' },
    ])
    expect(asObject(lockedVersions('gomod', JSON.stringify(parsed)))).toEqual({
      'example.com/lib': ['v1.2.3'],
      'example.com/new': ['v0.1.0'],
    })
  })
  it('reports a malformed go.mod file', () => {
    expect(() => goModJson('module\nrequire (\n')).toThrow('go mod edit -json failed')
  })
})

describe('ecosystemVersions', () => {
  it('merges every lockfile of an ecosystem and skips an absent file', () => {
    const files = {
      'frontend/bun.lock': bunLock([
        ['a', 'a@1.0.0'],
        ['b', 'b@2.0.0'],
      ]),
      '.github/renovate/bun.lock': bunLock([
        ['a', 'a@1.1.0'],
      ]),
      'desktop/rust/Cargo.lock': cargoLock([
        ['foo', '1.4.2'],
      ]),
      'backend/go.mod': 'module example.com/backend\n\ngo 1.25\n\nrequire example.com/lib v1.9.0\n',
      'site/go.mod': 'module example.com/site\n\ngo 1.25\n\nrequire example.com/lib v1.8.0\n',
    }
    const versions = ecosystemVersions(path => files[path] ?? null)
    expect(Object.fromEntries(Object.entries(versions).map(([ecosystem, map]) => [
      ecosystem,
      asObject(map),
    ]))).toEqual({
      npm: {
        a: ['1.0.0', '1.1.0'],
        b: ['2.0.0'],
      },
      crates: {
        foo: ['1.4.2'],
      },
      go: {
        'example.com/lib': ['v1.8.0', 'v1.9.0'],
      },
    })
  })
  it('returns empty maps when no lockfile exists', () => {
    expect(Object.values(ecosystemVersions(() => null)).every(map => map.size === 0)).toBe(true)
  })
})

describe('lockfileInput', () => {
  it('converts go.mod text and passes every other kind through', () => {
    expect(JSON.parse(lockfileInput('gomod', 'module example.com/app\n\ngo 1.25\n')).Module.Path).toBe('example.com/app')
    expect(lockfileInput('gomod', null)).toBeNull()
    expect(lockfileInput('bun', '{}')).toBe('{}')
  })
})

describe('DEPENDENCY_LOCKFILES', () => {
  it('lists every tracked lockfile once, and each one exists', () => {
    expect(new Set(DEPENDENCY_LOCKFILES.map(entry => entry.path)).size).toBe(DEPENDENCY_LOCKFILES.length)
    for (const { path } of DEPENDENCY_LOCKFILES)
      expect(readFileSync(resolve(root, path), 'utf8').length, path).toBeGreaterThan(0)
  })
})

describe('the lockfile parser under Node', () => {
  it('parses the real frontend bun.lock and desktop Cargo.lock', () => {
    const source = `const {lockedVersions}=await import(${JSON.stringify(new URL('./renovate-lockfile-versions.mjs', import.meta.url).href)}); const {readFileSync}=await import('node:fs'); const bun=lockedVersions('bun',readFileSync(process.argv[1],'utf8')); const cargo=lockedVersions('cargo',readFileSync(process.argv[2],'utf8')); console.log(JSON.stringify({runtime:process.versions.bun??'node',bun:bun.size,cargo:cargo.size,tauri:[...cargo.get('tauri')],api:[...bun.get('@tauri-apps/api')]}))`
    const result = spawnSync('node', [
      '--input-type=module',
      '-e',
      source,
      resolve(root, 'frontend/bun.lock'),
      resolve(root, 'desktop/rust/Cargo.lock'),
    ], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    const parsed = JSON.parse(result.stdout)
    const bun = lockedVersions('bun', readFileSync(resolve(root, 'frontend/bun.lock'), 'utf8'))
    const cargo = lockedVersions('cargo', readFileSync(resolve(root, 'desktop/rust/Cargo.lock'), 'utf8'))
    expect(parsed).toEqual({
      runtime: 'node',
      bun: bun.size,
      cargo: cargo.size,
      tauri: [
        ...cargo.get('tauri'),
      ],
      api: [
        ...bun.get('@tauri-apps/api'),
      ],
    })
    expect(parsed.bun).toBeGreaterThan(100)
    expect(parsed.cargo).toBeGreaterThan(100)
  })
})
