import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { ECOSYSTEMS } from './renovate-ecosystems.mjs'
import { DEPENDENCY_INPUT_FILES } from './renovate-inputs.mjs'
import { bunIdentity, cargoLine, cargoReplacementDowngrades, DEPENDENCY_LOCKFILES, dependencyLockfile, goModJson, goModVersions, introducedVersions, lockedVersions, readLockedVersions, readWorkingFile, versionIdentities } from './renovate-lockfile-versions.mjs'
import { scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)

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

const cargoVersions = packages => lockedVersions('cargo', cargoLock(packages))

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
  it('keeps the build metadata of a registry version', () => {
    expect(bunIdentity('pkg@1.0.0+build.5')).toEqual({ name: 'pkg', version: '1.0.0+build.5' })
    expect(bunIdentity('pkg@1.0.0-rc.1+sha.abc')).toEqual({ name: 'pkg', version: '1.0.0-rc.1+sha.abc' })
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
      'prefixed@v1.0.0',
      'loose@=1.0.0',
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
  it('rejects go.mod input, which only readLockedVersions converts', () => {
    expect(() => lockedVersions('gomod', goJson({}))).toThrow('Read go.mod through readLockedVersions.')
  })
  it('rejects an unknown kind and a malformed entry', () => {
    expect(() => lockedVersions('pip', '')).toThrow('Unknown lockfile kind')
    expect(() => lockedVersions('bun', '{ "packages": { "x": "x@1.0.0" } }')).toThrow('must be an array')
    expect(() => lockedVersions('cargo', '[[package]]\nname = "x"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n')).toThrow('Invalid cargo lockfile entry')
  })
  it('reads an empty lockfile as no versions', () => {
    expect(lockedVersions('bun', '{ "packages": {} }').size).toBe(0)
    expect(lockedVersions('bun', '{}').size).toBe(0)
    expect(lockedVersions('cargo', 'version = 4\n').size).toBe(0)
  })
})

describe('goModVersions', () => {
  it('reads Go requirements and versioned replacement targets, but not local replacements', () => {
    const versions = goModVersions(goJson({
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
  it('rejects a requirement without a version, and reads an empty module as no versions', () => {
    expect(() => goModVersions(JSON.stringify({
      Require: [
        { Path: 'example.com/x' },
      ],
    }))).toThrow('Invalid gomod lockfile entry')
    expect(goModVersions('{}').size).toBe(0)
  })
})

describe('introducedVersions', () => {
  it('reports only the versions that the head adds, for each kind, sorted', () => {
    expect(introducedVersions(lockedVersions('bun', bunLock([
      ['a', 'a@1.0.0'],
      ['b', 'b@1.0.0'],
    ])), lockedVersions('bun', bunLock([
      ['a', 'a@1.1.0'],
      ['b', 'b@1.0.0'],
      ['@s/c', '@s/c@2.0.0'],
    ])))).toEqual(['@s/c@2.0.0', 'a@1.1.0'])
    expect(introducedVersions(cargoVersions([
      ['foo', '1.4.1'],
    ]), cargoVersions([
      ['foo', '1.4.1'],
      ['foo', '2.0.0'],
    ]))).toEqual(['foo@2.0.0'])
    expect(introducedVersions(goModVersions(goJson({
      require: [
        ['example.com/lib', 'v1.9.0'],
      ],
    })), goModVersions(goJson({
      require: [
        ['example.com/lib/v2', 'v2.0.0'],
      ],
    })))).toEqual(['example.com/lib/v2@v2.0.0'])
  })
  it('reports nothing for an unchanged lockfile that holds two lines of one crate, or for a removal', () => {
    const versions = cargoVersions([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
    ])
    expect(introducedVersions(versions, versions)).toEqual([])
    expect(introducedVersions(versions, cargoVersions([
      ['bitflags', '2.13.1'],
    ]))).toEqual([])
  })
  it('treats an empty base as locking nothing', () => {
    expect(introducedVersions(new Map(), cargoVersions([
      ['foo', '1.0.0'],
    ]))).toEqual(['foo@1.0.0'])
    expect(introducedVersions(new Map(), new Map())).toEqual([])
  })
})

describe('versionIdentities', () => {
  it('lists each locked registry version as name@version', () => {
    expect(versionIdentities(cargoVersions([
      ['foo', '1.0.0'],
      ['foo', '2.0.0'],
      ['member', '0.1.0', null],
    ]))).toEqual(new Set(['foo@1.0.0', 'foo@2.0.0']))
    expect(versionIdentities(new Map())).toEqual(new Set())
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
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '1.4.2'],
    ]), cargoVersions([
      ['foo', '1.4.1'],
    ]))).toEqual([
      { name: 'foo', from: '1.4.2', to: '1.4.1' },
    ])
  })
  it('compares against the highest removed version of the line', () => {
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '1.3.0'],
      ['foo', '1.5.0'],
    ]), cargoVersions([
      ['foo', '1.4.0'],
    ]))).toEqual([
      { name: 'foo', from: '1.5.0', to: '1.4.0' },
    ])
  })
  it('accepts an addition beside a kept version and an upgrade of the lower of two versions', () => {
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '1.4.2'],
    ]), cargoVersions([
      ['foo', '1.4.1'],
      ['foo', '1.4.2'],
    ]))).toEqual([])
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '1.2.0'],
      ['foo', '1.4.2'],
    ]), cargoVersions([
      ['foo', '1.3.0'],
      ['foo', '1.4.2'],
    ]))).toEqual([])
  })
  it('never compares independent lines or unchanged duplicates', () => {
    const versions = cargoVersions([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
    ])
    expect(cargoReplacementDowngrades(versions, versions)).toEqual([])
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '2.0.0'],
      ['foo', '1.4.2'],
    ]), cargoVersions([
      ['foo', '2.0.0'],
      ['foo', '1.4.3'],
      ['foo', '0.9.0'],
    ]))).toEqual([])
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '0.3.5'],
    ]), cargoVersions([
      ['foo', '0.2.9'],
    ]))).toEqual([])
  })
  it('reports no downgrade without a base lockfile', () => {
    expect(cargoReplacementDowngrades(new Map(), cargoVersions([
      ['foo', '1.4.1'],
    ]))).toEqual([])
  })
  it('reports no downgrade for a removal without a replacement in the line', () => {
    expect(cargoReplacementDowngrades(cargoVersions([
      ['foo', '1.4.2'],
      ['bar', '1.0.0'],
    ]), cargoVersions([
      ['bar', '1.0.0'],
    ]))).toEqual([])
  })
})

describe('goModJson', () => {
  it('parses go.mod text with the Go toolchain', async () => {
    const parsed = JSON.parse(await goModJson('module example.com/app\n\ngo 1.25\n\nrequire example.com/lib v1.2.3\n\nreplace example.com/old => example.com/new v0.1.0\n'))
    expect(parsed.Require).toEqual([
      { Path: 'example.com/lib', Version: 'v1.2.3' },
    ])
    expect(asObject(goModVersions(JSON.stringify(parsed)))).toEqual({
      'example.com/lib': ['v1.2.3'],
      'example.com/new': ['v0.1.0'],
    })
  })
  it('reports a malformed go.mod file', async () => {
    await expect(goModJson('module\nrequire (\n')).rejects.toThrow('go mod edit -json failed')
  })
})

describe('readLockedVersions', () => {
  it('reads go.mod text through the Go toolchain and every other kind directly', async () => {
    expect(asObject(await readLockedVersions('gomod', 'module example.com/app\n\ngo 1.25\n\nrequire example.com/lib v1.2.3\n'))).toEqual({
      'example.com/lib': ['v1.2.3'],
    })
    expect(asObject(await readLockedVersions('bun', bunLock([
      ['a', 'a@1.0.0'],
    ])))).toEqual({
      a: ['1.0.0'],
    })
  })
  it('reads an absent file as no versions, without running go', async () => {
    const run = () => {
      throw new Error('An absent go.mod must not run go.')
    }
    expect(await readLockedVersions('gomod', null, { run })).toEqual(new Map())
    expect(await readLockedVersions('cargo', undefined)).toEqual(new Map())
  })
})

describe('DEPENDENCY_LOCKFILES', () => {
  it('lists every tracked lockfile once, and each one exists', () => {
    expect(new Set(DEPENDENCY_LOCKFILES.map(entry => entry.path)).size).toBe(DEPENDENCY_LOCKFILES.length)
    for (const { path } of DEPENDENCY_LOCKFILES)
      expect(readFileSync(resolve(root, path), 'utf8').length, path).toBeGreaterThan(0)
  })
  it('lists the lockfile of every manifest that the Renovate managers read, with its kind and ecosystem', () => {
    // A manager key without a lockfile here, such as a new manager, fails this test.
    const lockfileOf = {
      bun: manifest => ({
        path: join(dirname(manifest), 'bun.lock'),
        kind: 'bun',
        ecosystem: 'npm',
      }),
      cargo: manifest => ({
        path: join(dirname(manifest), 'Cargo.lock'),
        kind: 'cargo',
        ecosystem: 'crates',
      }),
      gomod: manifest => ({ path: manifest, kind: 'gomod', ecosystem: 'go' }),
      // versions.env holds direct pins, which Renovate's own selection checks.
      regex: () => null,
    }
    const expected = Object.entries(DEPENDENCY_INPUT_FILES).flatMap(([manager, manifests]) => {
      expect(lockfileOf, manager).toHaveProperty(manager)
      return manifests.map(lockfileOf[manager]).filter(Boolean)
    })
    const byPath = entries => [...entries].sort((a, b) => a.path.localeCompare(b.path))
    expect(byPath(DEPENDENCY_LOCKFILES)).toEqual(byPath(expected))
  })
  it('gives each lockfile the file name and the kind that the ecosystem table states', () => {
    for (const { path, kind, ecosystem } of DEPENDENCY_LOCKFILES) {
      expect(path.split('/').at(-1), path).toBe(ECOSYSTEMS[ecosystem].lockfile)
      expect(kind, path).toBe(ECOSYSTEMS[ecosystem].kind)
    }
  })
})

describe('dependencyLockfile', () => {
  it('finds a lockfile by its own path and by the manifest beside it', () => {
    for (const [path, lockfile] of [
      ['frontend/bun.lock', 'frontend/bun.lock'],
      ['frontend/package.json', 'frontend/bun.lock'],
      ['.github/renovate/package.json', '.github/renovate/bun.lock'],
      ['desktop/rust/Cargo.toml', 'desktop/rust/Cargo.lock'],
      ['desktop/rust/Cargo.lock', 'desktop/rust/Cargo.lock'],
      ['backend/go.mod', 'backend/go.mod'],
    ])
      expect(dependencyLockfile(path)?.path, path).toBe(lockfile)
  })
  it('returns null for an untracked path, another manifest name, and a missing path', () => {
    for (const path of [
      'other/bun.lock',
      'frontend/Cargo.toml',
      'backend/go.sum',
      'package.json',
      '',
      null,
      undefined,
    ])
      expect(dependencyLockfile(path), String(path)).toBeNull()
  })
})

describe('readWorkingFile', () => {
  it('returns the text of a file, and null for an absent file', () => {
    const directory = scratch.create('working-file')
    writeFileSync(join(directory, 'present.lock'), 'text')
    expect(readWorkingFile(directory, 'present.lock')).toBe('text')
    expect(readWorkingFile(directory, 'absent.lock')).toBeNull()
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
