import { spawnSync } from 'node:child_process'
import { describe, expect, it } from 'bun:test'
import { activeExemptions, authorizedClosure, authorizedFixes, buildAuthorizedMap, classifyGoMetadata, exactIdentity, exactPins, PREPARED_LOCKFILES, readAdvisories, resolveGoIdentity, splitIdentity, validateAuthorizedMap } from './renovate-exemptions.mjs'

function advisory(name, ecosystem, records, extra = {}) {
  return {
    ghsa_id: `GHSA-${name.replace(/\W/g, '').slice(0, 4).padEnd(4, 'x')}-test-test`,
    withdrawn_at: null,
    vulnerabilities: records.map(([range, fixed]) => ({
      package: { ecosystem, name },
      vulnerable_version_range: range,
      first_patched_version: fixed,
    })),
    ...extra,
  }
}

function advisoryReader(entries) {
  const calls = []
  return {
    calls,
    advisories: async (ecosystem, name) => {
      calls.push([ecosystem, name])
      return entries
    },
  }
}

function goReader(states) {
  const calls = []
  return {
    calls,
    goMetadata: async (module, version) => {
      const key = `${module}@${version}`
      calls.push(key)
      const status = states[key] ?? 'not-found'
      return {
        status,
        message: `${status}: ${key}`,
        info: status === 'found'
          ? { Path: module, Version: version }
          : undefined,
      }
    },
  }
}

describe('splitIdentity', () => {
  it('splits scoped, Go, and crate identities', () => {
    expect(splitIdentity('@s/y@1.0.1')).toEqual({ name: '@s/y', version: '1.0.1' })
    expect(splitIdentity('example.com/lib/v2@v2.0.0')).toEqual({ name: 'example.com/lib/v2', version: 'v2.0.0' })
    for (const identity of ['nameonly', '@scope/name', 'name@'])
      expect(() => splitIdentity(identity), identity).toThrow('Invalid package identity')
  })
})

describe('authorizedFixes', () => {
  it('returns the patched version for a vulnerable base and nothing for a base at or above it', async () => {
    const { advisories } = advisoryReader([
      advisory('pkg', 'npm', [
        ['< 1.2.3', '1.2.3'],
      ]),
    ])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.2.0'],
    }, { advisories })).toEqual([
      { name: 'pkg', version: '1.2.3' },
    ])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.2.3'],
    }, { advisories })).toEqual([])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.4.0'],
    }, { advisories })).toEqual([])
  })
  it('never returns a release newer than the patched version', async () => {
    // A later advisory fixes 1.3.0, but its range contains no base version.
    const { advisories } = advisoryReader([
      advisory('pkg', 'npm', [
        ['< 1.2.3', '1.2.3'],
      ]),
      advisory('pkg', 'npm', [
        ['>= 1.2.3, < 1.3.0', '1.3.0'],
      ]),
    ])
    const roots = await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.0.0', '1.1.0'],
    }, { advisories })
    expect(roots).toEqual([
      { name: 'pkg', version: '1.2.3' },
    ])
  })
  it('returns each patched version of two advisories, and only those', async () => {
    const { advisories } = advisoryReader([
      advisory('pkg', 'npm', [
        ['< 1.2.3', '1.2.3'],
      ]),
      advisory('pkg', 'npm', [
        ['>= 1.2.3, < 1.2.5', '1.2.5'],
      ]),
      advisory('pkg', 'npm', [
        ['>= 2.0.0, < 2.0.4', '2.0.4'],
      ]),
    ])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.2.0', '1.2.4'],
    }, { advisories })).toEqual([
      { name: 'pkg', version: '1.2.3' },
      { name: 'pkg', version: '1.2.5' },
    ])
  })
  it('ignores another package, another ecosystem, a withdrawn advisory, and a record without a fix', async () => {
    const { advisories } = advisoryReader([
      advisory('other', 'npm', [
        ['< 9.0.0', '9.0.0'],
      ]),
      advisory('pkg', 'rust', [
        ['< 9.0.0', '9.0.0'],
      ]),
      advisory('pkg', 'npm', [
        ['< 8.0.0', '8.0.0'],
      ], { withdrawn_at: '2026-10-01T00:00:00Z' }),
      advisory('pkg', 'npm', [
        ['< 7.0.0', null],
      ]),
    ])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.0.0'],
    }, { advisories })).toEqual([])
  })
  it('authorizes nothing for a record whose range is missing or blank', async () => {
    const { advisories } = advisoryReader([
      advisory('pkg', 'npm', [
        [null, '2.0.0'],
        ['', '3.0.0'],
        ['  ', '4.0.0'],
      ]),
    ])
    expect(await authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.0.0'],
    }, { advisories })).toEqual([])
  })
  it('returns nothing without reading advisories when the base locks no version', async () => {
    const reader = advisoryReader([
      advisory('pkg', 'npm', [
        ['< 1.2.3', '1.2.3'],
      ]),
    ])
    for (const baseVersions of [
      [],
      undefined,
      new Set(),
    ])
      expect(await authorizedFixes({ ecosystem: 'npm', name: 'pkg', baseVersions }, reader)).toEqual([])
    expect(reader.calls).toEqual([])
  })
  it('authorizes the Go patched version for a pseudo-version base within the range', async () => {
    const { advisories } = advisoryReader([
      advisory('example.com/lib', 'go', [
        ['< 1.0.1', '1.0.1'],
      ]),
    ])
    const go = goReader({ 'example.com/lib@v1.0.1': 'found' })
    expect(await authorizedFixes({
      ecosystem: 'go',
      name: 'example.com/lib',
      baseVersions: ['v1.0.1-0.20260615104152-545b12e0e8c7'],
    }, { advisories, goMetadata: go.goMetadata })).toEqual([
      { name: 'example.com/lib', version: 'v1.0.1' },
    ])
  })
  it('accepts the Dependabot object form of the patched version', async () => {
    const { advisories } = advisoryReader([
      advisory('glib', 'rust', [
        [
          '>= 0.15.0, < 0.20.0',
          { identifier: '0.20.0' },
        ],
      ]),
    ])
    expect(await authorizedFixes({
      ecosystem: 'crates',
      name: 'glib',
      baseVersions: ['0.18.5'],
    }, { advisories })).toEqual([
      { name: 'glib', version: '0.20.0' },
    ])
  })
  it('rejects an invalid patched version and an unknown ecosystem', async () => {
    const { advisories } = advisoryReader([
      advisory('pkg', 'npm', [
        ['< 1.2.3', 'next'],
      ]),
    ])
    await expect(authorizedFixes({
      ecosystem: 'npm',
      name: 'pkg',
      baseVersions: ['1.0.0'],
    }, { advisories })).rejects.toThrow('Invalid patched version')
    await expect(authorizedFixes({
      ecosystem: 'pip',
      name: 'pkg',
      baseVersions: ['1.0.0'],
    }, { advisories })).rejects.toThrow('Unknown advisory ecosystem')
  })
})

describe('resolveGoIdentity', () => {
  it('uses the one candidate of a path that carries the major version and never queries +incompatible', async () => {
    const go = goReader({ 'github.com/go-jose/go-jose/v4@v4.1.7': 'found' })
    expect(await resolveGoIdentity('github.com/go-jose/go-jose/v4', '4.1.7', go)).toEqual({ name: 'github.com/go-jose/go-jose/v4', version: 'v4.1.7' })
    expect(go.calls).toEqual(['github.com/go-jose/go-jose/v4@v4.1.7'])
  })
  it('falls back to the legacy +incompatible version when the major-suffixed module is absent', async () => {
    const go = goReader({ 'example.com/legacy@v2.0.0+incompatible': 'found' })
    expect(await resolveGoIdentity('example.com/legacy', '2.0.0', go)).toEqual({ name: 'example.com/legacy', version: 'v2.0.0+incompatible' })
    expect(go.calls).toEqual(['example.com/legacy/v2@v2.0.0', 'example.com/legacy@v2.0.0+incompatible'])
  })
  it('prefers the major-suffixed module when both identities exist', async () => {
    const go = goReader({ 'example.com/lib/v2@v2.0.0': 'found', 'example.com/lib@v2.0.0+incompatible': 'found' })
    expect(await resolveGoIdentity('example.com/lib', '2.0.0', go)).toEqual({ name: 'example.com/lib/v2', version: 'v2.0.0' })
    expect(go.calls).toEqual(['example.com/lib/v2@v2.0.0'])
  })
  it('moves a gopkg.in path to the major of the patched version', async () => {
    const go = goReader({ 'gopkg.in/yaml.v3@v3.0.1': 'found' })
    expect(await resolveGoIdentity('gopkg.in/yaml.v2', '3.0.1', go)).toEqual({ name: 'gopkg.in/yaml.v3', version: 'v3.0.1' })
    expect(go.calls).toEqual(['gopkg.in/yaml.v3@v3.0.1'])
  })
  it('stops at an error on the first candidate', async () => {
    const go = goReader({ 'example.com/lib/v2@v2.0.0': 'error', 'example.com/lib@v2.0.0+incompatible': 'found' })
    await expect(resolveGoIdentity('example.com/lib', '2.0.0', go)).rejects.toThrow('Cannot read Go module example.com/lib/v2@v2.0.0')
    expect(go.calls).toEqual(['example.com/lib/v2@v2.0.0'])
  })
  it('reports a version that no candidate publishes', async () => {
    const go = goReader({})
    await expect(resolveGoIdentity('example.com/lib', '2.0.0', go)).rejects.toThrow('no published module for example.com/lib 2.0.0')
    await expect(resolveGoIdentity('example.com/lib', 'next', go)).rejects.toThrow('Invalid Go patched version')
  })
})

describe('classifyGoMetadata', () => {
  const answer = info => ({
    status: 0,
    stdout: JSON.stringify(info),
    stderr: '',
  })
  const failure = Err => answer({
    Path: 'example.com/lib',
    Version: 'v1.2.3',
    Error: { Err },
  })
  it('reports a found version only when Go resolves the exact version', () => {
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', answer({ Path: 'example.com/lib', Version: 'v1.2.3' })).status).toBe('found')
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', answer({ Path: 'example.com/lib', Version: 'v1.2.4' })).status).toBe('error')
  })
  it('reports only an answer that states the version is absent as not found', () => {
    for (const message of [
      'example.com/lib@v1.2.3: reading http://127.0.0.1:1/example.com/lib/@v/v1.2.3.info: 404 Not Found',
      'example.com/lib@v1.2.3: reading http://127.0.0.1:1/example.com/lib/@v/v1.2.3.mod: 410 Gone',
      'example.com/lib@v1.2.3: reading file:///tmp/proxy/example.com/lib/@v/v1.2.3.info: no such file or directory',
      'example.com/lib@v1.2.3: invalid version: unknown revision v1.2.3',
      'example.com/lib/v4@v4.1.6+incompatible: invalid version: +incompatible suffix not allowed: module path includes a major version suffix, so major version must match',
    ])
      expect(classifyGoMetadata('example.com/lib', 'v1.2.3', failure(message)).status, message).toBe('not-found')
  })
  it('reports every transport, authentication, and server failure as an error', () => {
    for (const message of [
      'example.com/lib@v1.2.3: invalid version: git ls-remote -q origin in /tmp: exit status 128:\n\tfatal: unable to access \'https://example.com/lib/\': TLS handshake timeout',
      'example.com/lib@v1.2.3: invalid version: unknown revision v1.2.3: fatal: connection reset',
      'example.com/lib@v1.2.3: reading http://127.0.0.1:1/example.com/lib/@v/v1.2.3.info: 500 Internal Server Error',
      'example.com/lib@v1.2.3: reading http://127.0.0.1:1/example.com/lib/@v/v1.2.3.info: 403 Forbidden',
      'example.com/lib@v1.2.3: Get "https://127.0.0.1:1/example.com/lib/@v/v1.2.3.info": http: server gave HTTP response to HTTPS client',
      'example.com/lib@v1.2.3: invalid version: something else',
      'example.com/other@v1.2.3: reading http://127.0.0.1:1/example.com/other/@v/v1.2.3.info: 404 Not Found',
    ])
      expect(classifyGoMetadata('example.com/lib', 'v1.2.3', failure(message)).status, message).toBe('error')
  })
  it('reports a failed or malformed go list run as an error', () => {
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', { status: 1, stdout: '', stderr: 'go: broken' })).toEqual({ status: 'error', message: 'go: broken' })
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', { status: 0, stdout: 'not json', stderr: '' }).status).toBe('error')
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', {
      error: new Error('spawn go ENOENT'),
    })).toEqual({ status: 'error', message: 'spawn go ENOENT' })
    expect(classifyGoMetadata('example.com/lib', 'v1.2.3', answer({ Error: {} })).status).toBe('error')
  })
  it('escapes uppercase letters as the module proxy protocol does', () => {
    const message = 'github.com/Azure/lib@v1.2.3: reading http://127.0.0.1:1/github.com/!azure/lib/@v/v1.2.3.info: 404 Not Found'
    expect(classifyGoMetadata('github.com/Azure/lib', 'v1.2.3', failure(message)).status).toBe('not-found')
  })
})

describe('exactPins', () => {
  it('counts only an exact Cargo requirement of a normal or build dependency', async () => {
    const readers = {
      crateDependencies: async () => [
        { crate_id: 'exact', req: '=1.2.3', kind: 'normal' },
        { crate_id: 'spaced', req: '= 2.0.0', kind: 'build' },
        { crate_id: 'partial', req: '=1.2', kind: 'normal' },
        { crate_id: 'caret', req: '^1.2.3', kind: 'normal' },
        { crate_id: 'dev', req: '=3.0.0', kind: 'dev' },
      ],
    }
    expect(await exactPins('crates', 'root', '1.0.0', readers)).toEqual(['exact@1.2.3', 'spaced@2.0.0'])
  })
  it('counts an exact npm dependency or optional dependency, and an optional one replaces a regular one', async () => {
    const readers = {
      npmManifest: async () => ({
        dependencies: {
          'exact': '1.2.3',
          'tilde': '~1.2.3',
          '@s/y': '=1.0.1',
          'both': '^1.0.0',
        },
        optionalDependencies: { optional: 'v2.0.0', both: '1.0.4' },
        devDependencies: { dev: '1.0.0' },
        peerDependencies: { peer: '1.0.0' },
      }),
    }
    expect((await exactPins('npm', 'root', '1.0.0', readers)).sort()).toEqual([
      '@s/y@1.0.1',
      'both@1.0.4',
      'exact@1.2.3',
      'optional@2.0.0',
    ])
  })
  it('counts every requirement of a Go module', async () => {
    const readers = {
      goModText: async () => 'module example.com/root\n\ngo 1.25\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v0.2.0 // indirect\n)\n',
    }
    expect(await exactPins('go', 'example.com/root', 'v1.0.0', readers)).toEqual(['example.com/a@v1.0.0', 'example.com/b@v0.2.0'])
  })
  it('rejects an unknown ecosystem', async () => {
    await expect(exactPins('pip', 'x', '1.0.0', {})).rejects.toThrow('Unknown advisory ecosystem')
  })
})

describe('exactIdentity', () => {
  it('returns the identity of a requirement that admits one version, in each syntax', () => {
    expect(exactIdentity('crates', 'tauri', '=2.11.6')).toBe('tauri@2.11.6')
    expect(exactIdentity('crates', 'tauri', ' = 2.11.6 ')).toBe('tauri@2.11.6')
    expect(exactIdentity('npm', '@s/core', '1.2.3')).toBe('@s/core@1.2.3')
    expect(exactIdentity('npm', '@s/core', '=1.2.3')).toBe('@s/core@1.2.3')
  })
  it('returns null for a range or a value that is not a requirement, and rejects an unknown syntax', () => {
    for (const requirement of [
      '2',
      '^2.11.6',
      '>=1.0.0',
      '~1.2.3',
      undefined,
      null,
      3,
    ])
      expect(exactIdentity('crates', 'tauri', requirement), String(requirement)).toBeNull()
    for (const requirement of [
      '^1.2.3',
      '1.x',
      'latest',
      'workspace:*',
    ])
      expect(exactIdentity('npm', 'x', requirement), requirement).toBeNull()
    expect(() => exactIdentity('go', 'x', '1.0.0')).toThrow('Unknown requirement ecosystem')
  })
})

describe('authorizedClosure', () => {
  const graph = edges => ({
    npmManifest: async (name, version) => ({
      dependencies: Object.fromEntries((edges[`${name}@${version}`] ?? []).map(pin => [
        pin.slice(0, pin.lastIndexOf('@')),
        pin.slice(pin.lastIndexOf('@') + 1),
      ])),
    }),
  })
  it('follows exact pins transitively', async () => {
    const closures = await authorizedClosure([
      { ecosystem: 'npm', name: 'a', version: '1.0.0' },
    ], graph({
      'a@1.0.0': ['b@2.0.0'],
      'b@2.0.0': ['c@3.0.0'],
    }))
    expect(closures).toEqual(new Map([
      [
        'a@1.0.0',
        new Set(['b@2.0.0', 'c@3.0.0']),
      ],
    ]))
  })
  it('handles a cycle and keeps the root out of its own closure', async () => {
    const closures = await authorizedClosure([
      { ecosystem: 'npm', name: 'a', version: '1.0.0' },
    ], graph({
      'a@1.0.0': ['b@2.0.0'],
      'b@2.0.0': ['a@1.0.0'],
    }))
    expect(closures).toEqual(new Map([
      [
        'a@1.0.0',
        new Set(['b@2.0.0']),
      ],
    ]))
  })
  it('stops a Go closure at the requirements of the root, without reading the go.mod of a requirement', async () => {
    const read = []
    const closures = await authorizedClosure([
      { ecosystem: 'go', name: 'example.com/root', version: 'v1.0.0' },
    ], {
      goModText: async (module, version) => {
        read.push(`${module}@${version}`)
        return module === 'example.com/root'
          ? 'module example.com/root\n\ngo 1.25\n\nrequire example.com/a v1.0.0\n'
          : 'module example.com/a\n\ngo 1.25\n\nrequire example.com/old v0.1.0\n'
      },
    })
    expect(closures).toEqual(new Map([
      [
        'example.com/root@v1.0.0',
        new Set(['example.com/a@v1.0.0']),
      ],
    ]))
    expect(read).toEqual(['example.com/root@v1.0.0'])
  })
  it('returns an empty map for no roots and rejects mixed ecosystems', async () => {
    expect(await authorizedClosure([], {})).toEqual(new Map())
    await expect(authorizedClosure([
      { ecosystem: 'npm', name: 'a', version: '1.0.0' },
      { ecosystem: 'crates', name: 'a', version: '1.0.0' },
    ], {})).rejects.toThrow('one ecosystem')
  })
})

describe('activeExemptions', () => {
  const roots = {
    'c@1.0.0': ['d@1.0.0'],
    'a@1.0.0': ['b@2.0.0'],
  }
  const base = new Map()
  it('includes only the selected roots and their closures, with the roots sorted', () => {
    expect(activeExemptions(roots, {
      base,
      selected: root => root === 'a@1.0.0',
    })).toEqual({
      roots: ['a@1.0.0'],
      exempt: new Set(['a@1.0.0', 'b@2.0.0']),
    })
    expect(activeExemptions(roots, {
      base,
      selected: () => true,
    })).toEqual({
      roots: ['a@1.0.0', 'c@1.0.0'],
      exempt: new Set([
        'c@1.0.0',
        'd@1.0.0',
        'a@1.0.0',
        'b@2.0.0',
      ]),
    })
    expect(activeExemptions(undefined, {
      base,
      selected: () => true,
    })).toEqual({
      roots: [],
      exempt: new Set(),
    })
    expect(activeExemptions(roots, {
      base,
      selected: () => false,
    })).toEqual({
      roots: [],
      exempt: new Set(),
    })
  })
  it('skips a root that the base locks without asking the selection', () => {
    const asked = []
    const result = activeExemptions(roots, {
      base: new Map([
        [
          'a',
          new Set(['0.9.0', '1.0.0']),
        ],
        [
          'c',
          new Set(['0.9.0']),
        ],
      ]),
      selected: (root) => {
        asked.push(root)
        return true
      },
    })
    expect(result.roots).toEqual(['c@1.0.0'])
    expect(asked).toEqual(['c@1.0.0'])
  })
  it('rejects a call without the base versions or without a selection', () => {
    for (const options of [
      undefined,
      {
        selected: () => true,
      },
      {
        base: {},
        selected: () => true,
      },
      { base },
    ])
      expect(() => activeExemptions(roots, options), JSON.stringify(options)).toThrow('require the base versions and a selection')
  })
})

describe('buildAuthorizedMap', () => {
  const lockfiles = ['frontend/bun.lock', '.github/renovate/bun.lock', 'desktop/rust/Cargo.lock']
  it('keys the roots and their sorted closures by lockfile, with an entry for each lockfile', async () => {
    const readers = {
      advisories: async (ecosystem, name) => ({
        'npm/fixed-pkg': [
          advisory('fixed-pkg', 'npm', [
            ['< 1.2.3', '1.2.3'],
          ]),
        ],
        'crates/tauri-plugin-x': [
          advisory('tauri-plugin-x', 'rust', [
            ['< 2.4.1', '2.4.1'],
          ]),
        ],
      })[`${ecosystem}/${name}`] ?? [],
      npmManifest: async name => ({
        dependencies: name === 'fixed-pkg'
          ? { 'fixed-pkg-core': '1.2.3', 'loose': '^1.0.0' }
          : {},
      }),
      crateDependencies: async name => name === 'tauri-plugin-x'
        ? [
            { crate_id: 'tauri', req: '=2.11.6', kind: 'normal' },
            { crate_id: 'serde', req: '1', kind: 'normal' },
          ]
        : [],
    }
    expect(await buildAuthorizedMap([
      {
        lockfile: 'frontend/bun.lock',
        name: 'fixed-pkg',
        baseVersions: ['1.2.0'],
      },
      {
        lockfile: 'desktop/rust/Cargo.lock',
        name: 'tauri-plugin-x',
        baseVersions: ['2.4.0'],
      },
      {
        lockfile: 'desktop/rust/Cargo.lock',
        name: 'unaffected',
        baseVersions: ['1.0.0'],
      },
    ], readers, { lockfiles })).toEqual({
      'frontend/bun.lock': {
        'fixed-pkg@1.2.3': ['fixed-pkg-core@1.2.3'],
      },
      '.github/renovate/bun.lock': {},
      'desktop/rust/Cargo.lock': {
        'tauri-plugin-x@2.4.1': ['tauri@2.11.6'],
      },
    })
  })
  it('authorizes a package only from the base versions of its own lockfile, and reads a shared closure once', async () => {
    const manifests = []
    const readers = {
      advisories: async () => [
        advisory('shared', 'npm', [
          ['< 1.0.1', '1.0.1'],
        ]),
      ],
      npmManifest: async (name, version) => {
        manifests.push(`${name}@${version}`)
        return { dependencies: {} }
      },
    }
    expect(await buildAuthorizedMap([
      {
        lockfile: 'frontend/bun.lock',
        name: 'shared',
        baseVersions: ['1.0.0'],
      },
      // This lockfile locks only the patched version, so it authorizes nothing.
      {
        lockfile: '.github/renovate/bun.lock',
        name: 'shared',
        baseVersions: ['1.0.1'],
      },
    ], readers, {
      lockfiles: ['frontend/bun.lock', '.github/renovate/bun.lock'],
    })).toEqual({
      'frontend/bun.lock': { 'shared@1.0.1': [] },
      '.github/renovate/bun.lock': {},
    })
    expect(await buildAuthorizedMap([
      {
        lockfile: 'frontend/bun.lock',
        name: 'shared',
        baseVersions: ['1.0.0'],
      },
      {
        lockfile: '.github/renovate/bun.lock',
        name: 'shared',
        baseVersions: ['1.0.0'],
      },
    ], readers, {
      lockfiles: ['frontend/bun.lock', '.github/renovate/bun.lock'],
    })).toEqual({
      'frontend/bun.lock': { 'shared@1.0.1': [] },
      '.github/renovate/bun.lock': { 'shared@1.0.1': [] },
    })
    // One read for each map: the second map reads the shared root once for both lockfiles.
    expect(manifests).toEqual(['shared@1.0.1', 'shared@1.0.1'])
  })
  it('rejects an unknown lockfile, a package of a lockfile outside the map, and a call without lockfiles', async () => {
    const readers = {
      advisories: async () => {
        throw new Error('A rejected map must not read advisories.')
      },
    }
    await expect(buildAuthorizedMap([], readers, {
      lockfiles: ['frontend/bun.lock', 'other/bun.lock'],
    })).rejects.toThrow('Unknown dependency lockfile: other/bun.lock')
    await expect(buildAuthorizedMap([
      {
        lockfile: 'backend/go.mod',
        name: 'example.com/a',
        baseVersions: ['v1.0.0'],
      },
    ], readers, {
      lockfiles: ['frontend/bun.lock'],
    })).rejects.toThrow('belongs to no lockfile of the map: backend/go.mod')
    await expect(buildAuthorizedMap([], readers)).rejects.toThrow('requires the paths of its lockfiles')
  })
})

describe('buildAuthorizedMap when a read fails', () => {
  it('skips the package whose fixes and the root whose closure cannot be read, warns once, and keeps the others', async () => {
    const warnings = []
    const readers = {
      advisories: async (ecosystem, name) => {
        if (name === 'unreadable')
          throw new Error('Advisory read failed: 503')
        return [
          advisory(name, 'npm', [
            ['< 2.0.0', '2.0.0'],
          ]),
        ]
      },
      npmManifest: async (name) => {
        if (name === 'missing-fix')
          throw new Error('npm manifest read failed: 404')
        return { dependencies: {} }
      },
    }
    const pkg = (lockfile, name) => ({
      lockfile,
      name,
      baseVersions: ['1.0.0'],
    })
    expect(await buildAuthorizedMap([
      pkg('frontend/bun.lock', 'unreadable'),
      pkg('frontend/bun.lock', 'missing-fix'),
      pkg('.github/renovate/bun.lock', 'missing-fix'),
      pkg('frontend/bun.lock', 'kept'),
    ], readers, {
      lockfiles: ['frontend/bun.lock', '.github/renovate/bun.lock'],
      warn: message => warnings.push(message),
    })).toEqual({
      'frontend/bun.lock': { 'kept@2.0.0': [] },
      '.github/renovate/bun.lock': {},
    })
    expect(warnings).toEqual([
      'Skipped the security fixes of unreadable in frontend/bun.lock: Advisory read failed: 503',
      'Skipped the authorized fix missing-fix@2.0.0: npm manifest read failed: 404',
    ])
  })
})

describe('PREPARED_LOCKFILES', () => {
  it('lists every npm and crates lockfile, which a shim or the Tauri planner resolves, and no go.mod', () => {
    expect(PREPARED_LOCKFILES).toEqual([
      'frontend/bun.lock',
      '.github/renovate/bun.lock',
      'site/bun.lock',
      'desktop/rust/Cargo.lock',
    ])
  })
})

describe('validateAuthorizedMap', () => {
  it('returns a map of prepared lockfiles, including an empty one', () => {
    const authorized = {
      'frontend/bun.lock': {
        'x@1.1.0': ['x-core@1.1.0', '@s/y@2.0.0'],
      },
      'desktop/rust/Cargo.lock': {},
    }
    expect(validateAuthorizedMap(authorized)).toBe(authorized)
    expect(validateAuthorizedMap({})).toEqual({})
  })
  it('rejects a map that is not an object of lockfile maps', () => {
    for (const authorized of [null, undefined, [], 'map'])
      expect(() => validateAuthorizedMap(authorized), JSON.stringify(authorized)).toThrow('must map each lockfile path to its roots')
    for (const roots of [null, [], 'roots'])
      expect(() => validateAuthorizedMap({ 'frontend/bun.lock': roots }), JSON.stringify(roots)).toThrow('The authorized roots of frontend/bun.lock')
  })
  it('rejects a lockfile outside the prepared ones, and Go in particular', () => {
    for (const path of ['backend/go.mod', 'npm', 'other/bun.lock'])
      expect(() => validateAuthorizedMap({ [path]: {} }), path).toThrow(`unexpected lockfile: ${path}`)
  })
  it('rejects a root or a pin that is not a package identity, and a closure that is not an array', () => {
    expect(() => validateAuthorizedMap({
      'frontend/bun.lock': { 'x@1.0.0': 'x-core@1.0.0' },
    })).toThrow('must list package identities')
    expect(() => validateAuthorizedMap({
      'frontend/bun.lock': {
        'x@1.0.0': [1],
      },
    })).toThrow('must list package identities')
    expect(() => validateAuthorizedMap({
      'frontend/bun.lock': { x: [] },
    })).toThrow('Invalid package identity: x')
    expect(() => validateAuthorizedMap({
      'frontend/bun.lock': {
        'x@1.0.0': ['name@'],
      },
    })).toThrow('Invalid package identity: name@')
  })
})

describe('readAdvisories', () => {
  const page = (link, entries) => Response.json(entries, link
    ? {
        headers: { link },
      }
    : {})
  it('follows each cursor page with the token', async () => {
    const calls = []
    const request = async (url, init) => {
      calls.push({ url, authorization: init.headers.Authorization })
      return new URL(url).searchParams.get('after')
        ? page(null, [
            { ghsa_id: 'GHSA-page-two' },
          ])
        : page('<https://api.github.com/advisories?type=reviewed&ecosystem=npm&affects=cached-pkg&per_page=100&after=abc>; rel="next"', [
            { ghsa_id: 'GHSA-page-one' },
          ])
    }
    expect((await readAdvisories('npm', 'cached-pkg', {
      request,
      env: { GH_TOKEN: 'token-value' },
    })).map(entry => entry.ghsa_id)).toEqual(['GHSA-page-one', 'GHSA-page-two'])
    expect(calls).toHaveLength(2)
    expect(new URL(calls[0].url).searchParams.get('type')).toBe('reviewed')
    expect(new URL(calls[0].url).searchParams.get('affects')).toBe('cached-pkg')
    expect(calls.every(call => call.authorization === 'Bearer token-value')).toBe(true)
  })
  it('maps crates to the rust ecosystem, and prefers GITHUB_TOKEN to GH_TOKEN', async () => {
    const calls = []
    await readAdvisories('crates', 'a-crate', {
      env: { GITHUB_TOKEN: 'actions-token', GH_TOKEN: 'app-token' },
      request: async (url, init) => {
        calls.push({ url, authorization: init.headers.Authorization })
        return Response.json([])
      },
    })
    expect(new URL(calls[0].url).searchParams.get('ecosystem')).toBe('rust')
    expect(calls[0].authorization).toBe('Bearer actions-token')
  })
  it('requires a token before it reads anything', async () => {
    await expect(readAdvisories('npm', 'tokenless', {
      env: {},
      request: async () => {
        throw new Error('A read without a token must not reach the network.')
      },
    })).rejects.toThrow('A GitHub token is required.')
  })
  it('rejects a failed response, an invalid body, a link that the query does not allow, and an unknown ecosystem', async () => {
    const env = { GH_TOKEN: 'token-value' }
    await expect(readAdvisories('npm', 'failing-pkg', {
      request: async () => new Response('nope', { status: 502 }),
      env,
    })).rejects.toThrow('Advisory read failed: 502')
    await expect(readAdvisories('npm', 'object-pkg', {
      request: async () => Response.json({}),
      env,
    })).rejects.toThrow('invalid list')
    for (const link of [
      '<https://evil.example/advisories?type=reviewed&ecosystem=npm&affects=unsafe-pkg&per_page=100&after=x>; rel="next"',
      '<https://api.github.com/advisories?type=reviewed&ecosystem=npm&affects=other-pkg&per_page=100&after=x>; rel="next"',
      '<https://api.github.com/advisories?type=reviewed&ecosystem=npm&affects=unsafe-pkg&per_page=100>; rel="next"',
      '<https://api.github.com/advisories?type=reviewed&ecosystem=npm&affects=unsafe-pkg&per_page=100&after=x&page=2>; rel="next"',
    ]) {
      await expect(readAdvisories('npm', 'unsafe-pkg', {
        request: async () => page(link, []),
        env,
      }), link).rejects.toThrow('Unsafe or ambiguous GitHub pagination link.')
    }
    await expect(readAdvisories('pip', 'x', { env })).rejects.toThrow('Unknown advisory ecosystem')
  })
  it('stops a cursor cycle without a second read of the repeated page', async () => {
    let requests = 0
    const link = '<https://api.github.com/advisories?type=reviewed&ecosystem=npm&affects=cycle&per_page=100&after=same>; rel="next"'
    await expect(readAdvisories('npm', 'cycle', {
      env: { GH_TOKEN: 'token-value' },
      request: async () => {
        requests++
        return page(link, [])
      },
    })).rejects.toThrow('Advisory pagination cycle detected.')
    expect(requests).toBe(2)
  })
})

describe('the exemption policy under Node', () => {
  it('evaluates active exemptions with the same results', () => {
    const source = `const m=await import(${JSON.stringify(new URL('./renovate-exemptions.mjs', import.meta.url).href)}); console.log(JSON.stringify({exempt:[...m.activeExemptions({'a@1.0.0':['b@2.0.0'],'c@1.0.0':[]},{base:new Map(),selected:k=>k==='a@1.0.0'}).exempt]}))`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toEqual({
      exempt: ['a@1.0.0', 'b@2.0.0'],
    })
  })
})
