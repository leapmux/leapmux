import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { annotations, checkDependencyAge, main, parseJsonStream, readGoTimes, SECURITY_BRANCHES, securityExemptions, timeReaders } from './check-dependency-age.mjs'
import { introducedVersions, lockedVersions } from './renovate-lockfile-versions.mjs'
import { crateIndexResponse, git, gitRepository, scratchDirectories, writeFiles } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)

const now = Date.parse('2026-10-31T00:00:00Z')
const fourDays = '2026-10-27T00:00:00Z'
const oneHour = '2026-10-30T23:00:00Z'

const bunLock = identities => `{\n  "lockfileVersion": 1,\n  "packages": {\n${identities.map((identity, index) => `    "entry-${index}": [${JSON.stringify(identity)}, "", {}, "sha512-x"],`).join('\n')}\n  },\n}\n`
function cargoLock(identities) {
  return `version = 4\n\n${identities.map((identity) => {
    const [name, version] = identity.split('@')
    return `[[package]]\nname = "${name}"\nversion = "${version}"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n`
  }).join('\n')}`
}
const goMod = requirements => `module github.com/leapmux/leapmux\n\ngo 1.25\n\nrequire (\n${requirements.map(identity => `\t${identity.replace('@', ' ')}`).join('\n')}\n)\n`

// A base commit, and a head commit of the pull request on top of it.
function repository(base, head) {
  const checkout = scratch.create('check-dependency-age')
  const baseCommit = gitRepository(checkout, {
    files: {
      'renovate.json': readFileSync(resolve(root, 'renovate.json')),
      ...base,
    },
  })
  writeFiles(checkout, head)
  git(checkout, ['add', '--all'])
  git(checkout, [
    'commit',
    '--quiet',
    '--allow-empty',
    '--message',
    'head',
  ])
  return {
    checkout,
    base: baseCommit,
    head: git(checkout, ['rev-parse', 'HEAD']),
  }
}

function offline() {
  throw new Error('Unexpected network read.')
}
const offlineReaders = {
  advisories: offline,
  goMetadata: offline,
  crateDependencies: offline,
  npmManifest: offline,
  goModText: offline,
}
const offlineTimes = { npmTime: offline, crateTime: offline, goTimes: offline }

function timeTable(table) {
  const reads = []
  return {
    reads,
    npmTime: async (name, version) => {
      reads.push(`${name}@${version}`)
      return table[`${name}@${version}`] ?? null
    },
    crateTime: async (name, version) => {
      reads.push(`${name}@${version}`)
      return table[`${name}@${version}`] ?? null
    },
    goTimes: async (identities) => {
      reads.push(...identities)
      return new Map(identities.filter(identity => table[identity]).map(identity => [identity, table[identity]]))
    },
  }
}

function advisoryReaders(advisories, { pins = {}, go = {} } = {}) {
  return {
    advisories: async (ecosystem, name) => advisories.filter(advisory => advisory.vulnerabilities.some(record => record.package.name === name)),
    npmManifest: async (name, version) => ({
      dependencies: Object.fromEntries((pins[`${name}@${version}`] ?? []).map(pin => [
        pin.slice(0, pin.lastIndexOf('@')),
        pin.slice(pin.lastIndexOf('@') + 1),
      ])),
    }),
    crateDependencies: async (name, version) => (pins[`${name}@${version}`] ?? []).map(pin => ({ crate_id: pin.split('@')[0], req: `=${pin.split('@')[1]}`, kind: 'normal' })),
    goMetadata: async (module, version) => ({
      status: go[`${module}@${version}`] ?? 'not-found',
      message: 'fixture',
      info: { Path: module, Version: version },
    }),
    goModText: async module => `module ${module}\n\ngo 1.25\n`,
  }
}

// The open Dependabot alerts of packages on one dependency file.
function alertsOn(manifest, ecosystem, ...names) {
  return names.map(name => ({
    state: 'open',
    dependency: { manifest_path: manifest },
    security_vulnerability: {
      package: { ecosystem, name },
    },
  }))
}

function noAlerts() {
  throw new Error('Unexpected Dependabot alert read.')
}

function advisory(ecosystem, name, range, fixed) {
  return {
    vulnerabilities: [
      {
        package: { ecosystem, name },
        vulnerable_version_range: range,
        first_patched_version: fixed,
      },
    ],
  }
}

describe('checkDependencyAge', () => {
  it('checks a pull request from any branch, so a person who adds a young version gets a violation', async () => {
    const { checkout, base } = repository({
      'frontend/bun.lock': bunLock(['a@1.0.0']),
    }, {
      'frontend/bun.lock': bunLock(['a@1.1.0']),
    })
    for (const headRef of ['feature/age', 'main', '']) {
      expect((await checkDependencyAge({
        base,
        headRef,
        checkout,
        now,
        readers: offlineReaders,
        openAlerts: noAlerts,
        times: timeTable({ 'a@1.1.0': oneHour }),
      })).map(violation => violation.identity), headRef).toEqual(['a@1.1.0'])
    }
  })
  it('accepts a Cargo downgrade on a branch that a person owns, who may lower a version on purpose', async () => {
    const { checkout, base } = repository({
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.4.2']),
    }, {
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.4.1']),
    })
    expect(await checkDependencyAge({
      base,
      headRef: 'feature/pin-foo',
      checkout,
      now,
      readers: offlineReaders,
      openAlerts: noAlerts,
      times: timeTable({ 'foo@1.4.1': fourDays }),
    })).toEqual([])
  })
  it('requires the head ref before it reads anything', async () => {
    for (const headRef of [undefined, null, 42]) {
      await expect(checkDependencyAge({
        base: 'missing',
        headRef,
        checkout: '/nonexistent',
        readers: offlineReaders,
        openAlerts: noAlerts,
        times: offlineTimes,
      }), String(headRef)).rejects.toThrow('requires the head ref')
    }
  })
  it('reads no registry when no lockfile changes', async () => {
    const files = {
      'frontend/bun.lock': bunLock(['a@1.0.0']),
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.0.0']),
    }
    const { checkout, base } = repository(files, {})
    expect(await checkDependencyAge({
      base,
      headRef: 'renovate/lockfiles',
      checkout,
      now,
      readers: offlineReaders,
      times: offlineTimes,
    })).toEqual([])
  })
  it('reads the head lockfiles from the head revision, never from the working tree', async () => {
    const { checkout, base, head } = repository({
      'frontend/bun.lock': bunLock(['a@1.0.0']),
    }, {
      'frontend/bun.lock': bunLock(['a@1.1.0']),
    })
    // The working tree holds the trusted base tools in CI, not the head of the PR.
    writeFiles(checkout, {
      'frontend/bun.lock': bunLock(['a@1.0.0', 'young@1.0.0']),
    })
    const times = timeTable({ 'a@1.1.0': fourDays, 'young@1.0.0': oneHour })
    expect(await checkDependencyAge({
      base,
      head,
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: offlineReaders,
      times,
    })).toEqual([])
    expect(times.reads).toEqual(['a@1.1.0'])
    await expect(checkDependencyAge({
      base,
      head: 'missing-revision',
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: offlineReaders,
      times,
    })).rejects.toThrow('Cannot read the git revision missing-revision')
  })
  it('states an age with a buffer in the words of the dashboard', async () => {
    const { checkout, base } = repository({
      'renovate.json': JSON.stringify({ minimumReleaseAge: '3 days', minimumReleaseAgeBuffer: '30 minutes' }),
      'frontend/bun.lock': bunLock(['a@1.0.0']),
    }, {
      'frontend/bun.lock': bunLock(['a@1.1.0']),
    })
    expect(await checkDependencyAge({
      base,
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: offlineReaders,
      times: timeTable({ 'a@1.1.0': oneHour }),
    })).toEqual([
      { file: 'frontend/bun.lock', identity: 'a@1.1.0', message: 'a@1.1.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days 30 minutes ago.' },
    ])
  })
  it('accepts a version four days old and rejects one an hour old with its file and identity', async () => {
    const { checkout, base } = repository({
      'frontend/bun.lock': bunLock(['a@1.0.0']),
    }, {
      'frontend/bun.lock': bunLock(['a@1.1.0', '@s/b@2.0.0']),
    })
    const times = timeTable({ 'a@1.1.0': fourDays, '@s/b@2.0.0': oneHour })
    const violations = await checkDependencyAge({
      base,
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: offlineReaders,
      times,
    })
    expect(violations).toEqual([
      { file: 'frontend/bun.lock', identity: '@s/b@2.0.0', message: '@s/b@2.0.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days ago.' },
    ])
    expect(annotations(violations)).toEqual(['::error file=frontend/bun.lock::@s/b@2.0.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days ago.'])
    expect(times.reads.sort()).toEqual(['@s/b@2.0.0', 'a@1.1.0'])
  })
  it('rejects a version without a release timestamp', async () => {
    const { checkout, base } = repository({
      'desktop/rust/Cargo.lock': cargoLock([]),
    }, {
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.0.0']),
    })
    expect(await checkDependencyAge({
      base,
      headRef: 'renovate/rust',
      checkout,
      now,
      readers: offlineReaders,
      times: timeTable({}),
    })).toEqual([
      { file: 'desktop/rust/Cargo.lock', identity: 'foo@1.0.0', message: 'foo@1.0.0 has no release timestamp.' },
    ])
  })
  it('rejects a Cargo downgrade on every Renovate branch, even when every version is old enough', async () => {
    const { checkout, base } = repository({
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.4.2']),
    }, {
      'desktop/rust/Cargo.lock': cargoLock(['foo@1.4.1']),
    })
    for (const headRef of ['renovate/rust', 'renovate/security']) {
      const violations = await checkDependencyAge({
        base,
        headRef,
        checkout,
        now,
        readers: advisoryReaders([]),
        openAlerts: noAlerts,
        times: timeTable({ 'foo@1.4.1': fourDays }),
      })
      expect(violations, headRef).toEqual([
        { file: 'desktop/rust/Cargo.lock', identity: 'foo@1.4.1', message: 'cargo would downgrade foo from 1.4.2 to 1.4.1.' },
      ])
    }
  })
  it('checks every version of a lockfile that the base lacks, and reads nothing for a removed lockfile', async () => {
    const { checkout, base } = repository({
      'desktop/go/go.mod': goMod(['example.com/desktop@v1.0.0']),
      'site/go.mod': goMod(['example.com/site@v1.0.0']),
    }, {
      '.github/renovate/bun.lock': bunLock(['renovate@44.0.0', 'json5@2.2.3']),
      'desktop/go/go.mod': null,
      'site/go.mod': null,
    })
    const times = timeTable({ 'renovate@44.0.0': oneHour, 'json5@2.2.3': fourDays })
    expect(await checkDependencyAge({
      base,
      headRef: 'renovate/renovate',
      checkout,
      now,
      readers: offlineReaders,
      times,
    })).toEqual([
      { file: '.github/renovate/bun.lock', identity: 'renovate@44.0.0', message: 'renovate@44.0.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days ago.' },
    ])
    expect(times.reads.sort()).toEqual(['json5@2.2.3', 'renovate@44.0.0'])
  })
  it('fails when the base revision has no renovate.json', async () => {
    const { checkout, base } = repository({
      'renovate.json': null,
      'frontend/bun.lock': bunLock(['a@1.0.0']),
    }, {})
    await expect(checkDependencyAge({
      base,
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: offlineReaders,
      times: offlineTimes,
    })).rejects.toThrow('has no renovate.json')
  })
  it('rejects the patched version an hour old on a routine branch', async () => {
    const { checkout, base } = repository({
      'frontend/bun.lock': bunLock(['fixed-pkg@1.2.0']),
    }, {
      'frontend/bun.lock': bunLock(['fixed-pkg@1.2.3']),
    })
    const violations = await checkDependencyAge({
      base,
      headRef: 'renovate/frontend',
      checkout,
      now,
      readers: advisoryReaders([
        advisory('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
      ]),
      openAlerts: noAlerts,
      times: timeTable({ 'fixed-pkg@1.2.3': oneHour }),
    })
    expect(violations.map(violation => violation.identity)).toEqual(['fixed-pkg@1.2.3'])
  })

  describe('on renovate/security', () => {
    const fix = [
      advisory('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
    ]
    const pins = {
      'fixed-pkg@1.2.3': ['fixed-pkg-core@1.2.3'],
    }
    const alerted = alertsOn('frontend/package.json', 'npm', 'fixed-pkg', 'b', 'pkg', 'other')
    const check = async (base, head, readers = advisoryReaders(fix, { pins }), table = {}, alerts = alerted) => {
      const fixture = repository({
        'frontend/bun.lock': bunLock(base),
      }, {
        'frontend/bun.lock': bunLock(head),
      })
      const times = timeTable(table)
      const violations = await checkDependencyAge({
        ...fixture,
        headRef: 'renovate/security',
        now,
        readers,
        openAlerts: async () => alerts,
        times,
      })
      return {
        identities: violations.map(violation => violation.identity),
        reads: times.reads,
      }
    }
    it('accepts the exact patched root and its exact pins', async () => {
      expect(await check(['fixed-pkg@1.2.0', 'fixed-pkg-core@1.2.0'], ['fixed-pkg@1.2.3', 'fixed-pkg-core@1.2.3'])).toEqual({
        identities: [],
        reads: ['fixed-pkg-core@1.2.3', 'fixed-pkg@1.2.3'],
      })
    })
    it('reads no advisory when every new version is old enough', async () => {
      const advisoryReads = []
      const readers = {
        ...advisoryReaders(fix, { pins }),
        advisories: async (ecosystem, name) => {
          advisoryReads.push(name)
          return fix
        },
      }
      // Null alerts fail any read of them, so the check reads no alert either.
      expect((await check(['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.3'], readers, { 'fixed-pkg@1.2.3': fourDays }, null)).identities).toEqual([])
      expect(advisoryReads).toEqual([])
    })
    it('exempts nothing for a package without an open alert, although an advisory lists its fix', async () => {
      for (const alerts of [
        [],
        alertsOn('frontend/package.json', 'npm', 'other'),
        // An alert on another lockfile authorizes nothing in this one.
        alertsOn('.github/renovate/package.json', 'npm', 'fixed-pkg'),
        alertsOn('frontend/package.json', 'npm', 'fixed-pkg').map(alert => ({ ...alert, state: 'dismissed' })),
      ]) {
        expect((await check(['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.3'], undefined, { 'fixed-pkg@1.2.3': oneHour }, alerts)).identities, JSON.stringify(alerts)).toEqual(['fixed-pkg@1.2.3'])
      }
    })
    it('fails when it cannot read the open alerts for a young version', async () => {
      const fixture = repository({
        'frontend/bun.lock': bunLock(['fixed-pkg@1.2.0']),
      }, {
        'frontend/bun.lock': bunLock(['fixed-pkg@1.2.3']),
      })
      await expect(checkDependencyAge({
        ...fixture,
        headRef: 'renovate/security',
        now,
        readers: advisoryReaders(fix),
        openAlerts: async () => {
          throw new Error('Dependabot alert read failed: 403')
        },
        times: timeTable({ 'fixed-pkg@1.2.3': oneHour }),
      })).rejects.toThrow('Dependabot alert read failed: 403')
    })
    it('passes an old-enough fix when its advisory cannot be read, and warns about the skipped fix', async () => {
      const warnings = []
      const fixture = repository({
        'frontend/bun.lock': bunLock(['fixed-pkg@1.2.0', 'other@1.0.0']),
      }, {
        'frontend/bun.lock': bunLock(['fixed-pkg@1.2.3', 'other@1.0.1']),
      })
      const readers = {
        ...advisoryReaders(fix, { pins }),
        advisories: async (ecosystem, name) => {
          if (name === 'other')
            throw new Error('Advisory read failed: 503')
          return fix
        },
      }
      const options = {
        ...fixture,
        headRef: 'renovate/security',
        now,
        readers,
        openAlerts: async () => alerted,
        warn: message => warnings.push(message),
      }
      expect(await checkDependencyAge({
        ...options,
        times: timeTable({ 'fixed-pkg@1.2.3': oneHour, 'other@1.0.1': fourDays }),
      })).toEqual([])
      expect(warnings).toEqual(['Skipped the security fixes of other in frontend/bun.lock: Advisory read failed: 503'])
      // The skipped package exempts nothing, so its young version still fails.
      const young = await checkDependencyAge({
        ...options,
        times: timeTable({ 'fixed-pkg@1.2.3': oneHour, 'other@1.0.1': oneHour }),
      })
      expect(young.map(violation => violation.identity)).toEqual(['other@1.0.1'])
    })
    it('rejects a young pin of an authorized root that the PR leaves unchanged', async () => {
      const readers = advisoryReaders([
        advisory('npm', 'b', '< 1.1.0', '1.1.0'),
      ], {
        pins: {
          'b@1.1.0': ['c@2.0.0'],
        },
      })
      expect((await check(['b@1.0.0'], ['b@1.0.0', 'c@2.0.0'], readers, { 'c@2.0.0': oneHour })).identities).toEqual(['c@2.0.0'])
    })
    it('rejects a young pin of an authorized root that the base already locks', async () => {
      // The PR changes b, so the check authorizes b@1.1.0. The base already locks that
      // root, so the PR does not introduce it, and the root exempts nothing.
      const readers = advisoryReaders([
        advisory('npm', 'b', '< 1.1.0', '1.1.0'),
      ], {
        pins: {
          'b@1.1.0': ['c@2.0.0'],
        },
      })
      expect((await check(['b@1.0.0', 'b@1.1.0'], ['b@1.1.0', 'c@2.0.0'], readers, { 'c@2.0.0': oneHour })).identities).toEqual(['c@2.0.0'])
    })
    it('rejects a young version newer than the patched version', async () => {
      expect((await check(['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.4'], undefined, { 'fixed-pkg@1.2.4': oneHour })).identities).toEqual(['fixed-pkg@1.2.4'])
    })
    it('rejects a young companion that the patched version does not pin exactly', async () => {
      expect((await check(['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.3', 'companion@3.0.0'], undefined, { 'companion@3.0.0': oneHour })).identities).toEqual(['companion@3.0.0'])
    })
    it('exempts the roots of two advisories, and only those', async () => {
      const readers = advisoryReaders([
        advisory('npm', 'pkg', '< 1.2.3', '1.2.3'),
        advisory('npm', 'pkg', '>= 1.2.4, < 1.2.5', '1.2.5'),
      ])
      expect(await check(['pkg@1.2.0', 'pkg@1.2.4'], ['pkg@1.2.3', 'pkg@1.2.5', 'pkg@1.2.6'], readers, { 'pkg@1.2.6': oneHour })).toEqual({
        identities: ['pkg@1.2.6'],
        reads: ['pkg@1.2.3', 'pkg@1.2.5', 'pkg@1.2.6'],
      })
    })
  })

  describe('on renovate/go-security', () => {
    const check = async (base, head, advisories, go, table = {}, alerted = true) => {
      const fixture = repository({
        'backend/go.mod': goMod(base),
      }, {
        'backend/go.mod': goMod(head),
      })
      const times = timeTable(table)
      const violations = await checkDependencyAge({
        ...fixture,
        headRef: 'renovate/go-security',
        now,
        readers: advisoryReaders(advisories, { go }),
        openAlerts: async () => alerted
          ? alertsOn('backend/go.mod', 'go', ...new Set(advisories.flatMap(entry => entry.vulnerabilities.map(record => record.package.name))))
          : [],
        times,
      })
      return {
        identities: violations.map(violation => violation.identity),
        reads: times.reads,
      }
    }
    it('accepts a move to the major-suffixed module path', async () => {
      expect(await check(['example.com/lib@v1.9.0'], ['example.com/lib/v2@v2.0.0'], [
        advisory('go', 'example.com/lib', '< 2.0.0', '2.0.0'),
      ], { 'example.com/lib/v2@v2.0.0': 'found' })).toEqual({
        identities: [],
        reads: ['example.com/lib/v2@v2.0.0'],
      })
    })
    it('accepts a gopkg.in move to the next major', async () => {
      expect((await check(['gopkg.in/yaml.v2@v2.4.0'], ['gopkg.in/yaml.v3@v3.0.1'], [
        advisory('go', 'gopkg.in/yaml.v2', '< 3.0.1', '3.0.1'),
      ], { 'gopkg.in/yaml.v3@v3.0.1': 'found' })).identities).toEqual([])
    })
    it('accepts a published +incompatible version', async () => {
      expect((await check(['example.com/legacy@v1.0.0'], ['example.com/legacy@v2.0.0+incompatible'], [
        advisory('go', 'example.com/legacy', '< 2.0.0', '2.0.0'),
      ], { 'example.com/legacy@v2.0.0+incompatible': 'found' })).identities).toEqual([])
    })
    it('authorizes the patched version of a pseudo-version base within the range', async () => {
      expect((await check(['example.com/pseudo@v1.0.1-0.20260615104152-545b12e0e8c7'], ['example.com/pseudo@v1.0.1'], [
        advisory('go', 'example.com/pseudo', '< 1.0.1', '1.0.1'),
      ], { 'example.com/pseudo@v1.0.1': 'found' })).identities).toEqual([])
    })
    it('rejects a young first patched version without an open alert, as no shim guards Go', async () => {
      expect((await check(['example.com/lib@v1.9.0'], ['example.com/lib/v2@v2.0.0'], [
        advisory('go', 'example.com/lib', '< 2.0.0', '2.0.0'),
      ], { 'example.com/lib/v2@v2.0.0': 'found' }, {}, false)).identities).toEqual(['example.com/lib/v2@v2.0.0'])
    })
    it('still rejects another young module in the same PR', async () => {
      expect(await check(['example.com/lib@v1.9.0'], ['example.com/lib/v2@v2.0.0', 'example.com/other@v0.2.0'], [
        advisory('go', 'example.com/lib', '< 2.0.0', '2.0.0'),
      ], { 'example.com/lib/v2@v2.0.0': 'found' }, { 'example.com/other@v0.2.0': oneHour })).toEqual({
        identities: ['example.com/other@v0.2.0'],
        reads: ['example.com/lib/v2@v2.0.0', 'example.com/other@v0.2.0'],
      })
    })
  })
})

describe('securityExemptions', () => {
  // A changed lockfile, as checkDependencyAge passes it.
  const changed = (path, ecosystem, kind, lock, before, after) => {
    const baseVersions = lockedVersions(kind, lock(before))
    const headVersions = lockedVersions(kind, lock(after))
    return {
      path,
      ecosystem,
      baseVersions,
      headVersions,
      introduced: introducedVersions(baseVersions, headVersions),
    }
  }
  const recording = (readers) => {
    const asked = []
    return {
      asked,
      readers: {
        ...readers,
        advisories: async (ecosystem, name) => {
          asked.push(`${ecosystem}:${name}`)
          return readers.advisories(ecosystem, name)
        },
      },
    }
  }
  it('exempts an introduced root and its closure, from the base versions of the changed lockfile alone', async () => {
    const file = changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['fixed-pkg@1.2.0', 'fixed-pkg-core@1.2.0', 'steady@1.0.0'], ['fixed-pkg@1.2.3', 'fixed-pkg-core@1.2.3', 'steady@1.0.0'])
    const { asked, readers } = recording(advisoryReaders([
      advisory('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
    ], {
      pins: {
        'fixed-pkg@1.2.3': ['fixed-pkg-core@1.2.3'],
      },
    }))
    const alerts = alertsOn('frontend/bun.lock', 'npm', 'fixed-pkg', 'fixed-pkg-core', 'steady')
    expect(await securityExemptions([file], { alerts, readers })).toEqual(new Map([
      [
        'frontend/bun.lock',
        new Set(['fixed-pkg@1.2.3', 'fixed-pkg-core@1.2.3']),
      ],
    ]))
    // An unchanged package asks for no advisory, even with an open alert.
    expect(asked.sort()).toEqual(['npm:fixed-pkg', 'npm:fixed-pkg-core'])
  })
  it('keys each exemption by its lockfile, and asks about a removed package', async () => {
    const npm = changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['gone@1.0.0'], ['added@1.0.0'])
    const crates = changed('desktop/rust/Cargo.lock', 'crates', 'cargo', cargoLock, ['fix@2.0.0', 'pin@1.0.4'], ['fix@2.0.1', 'pin@1.0.5'])
    const { asked, readers } = recording(advisoryReaders([
      advisory('rust', 'fix', '< 2.0.1', '2.0.1'),
    ], {
      pins: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
    }))
    const alerts = [
      ...alertsOn('frontend/package.json', 'npm', 'gone', 'added'),
      ...alertsOn('desktop/rust/Cargo.lock', 'rust', 'fix', 'pin'),
    ]
    expect(await securityExemptions([npm, crates], { alerts, readers })).toEqual(new Map([
      [
        'frontend/bun.lock',
        new Set(),
      ],
      [
        'desktop/rust/Cargo.lock',
        new Set(['fix@2.0.1', 'pin@1.0.5']),
      ],
    ]))
    // A package that only the head locks has no base version, so it has no fix to authorize.
    expect(asked.sort()).toEqual(['crates:fix', 'crates:pin', 'npm:gone'])
  })
  it('exempts nothing for a fix that the lockfile does not introduce or that the base already locks', async () => {
    const readers = advisoryReaders([
      advisory('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
    ])
    const alerts = alertsOn('frontend/bun.lock', 'npm', 'fixed-pkg')
    const none = new Map([
      [
        'frontend/bun.lock',
        new Set(),
      ],
    ])
    // The head moves past the patched version, so the authorized root is not introduced.
    expect(await securityExemptions([
      changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.4']),
    ], { alerts, readers })).toEqual(none)
    // The base already locks the patched version beside the vulnerable one.
    expect(await securityExemptions([
      changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['fixed-pkg@1.2.0', 'fixed-pkg@1.2.3'], ['fixed-pkg@1.2.3']),
    ], { alerts, readers })).toEqual(none)
  })
  it('reads no advisory and exempts nothing without an open alert on the changed lockfile', async () => {
    const { asked, readers } = recording(advisoryReaders([
      advisory('npm', 'fixed-pkg', '< 1.2.3', '1.2.3'),
    ]))
    const file = changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.3'])
    for (const alerts of [
      [],
      alertsOn('.github/renovate/bun.lock', 'npm', 'fixed-pkg'),
      undefined,
    ]) {
      expect(await securityExemptions([file], { alerts, readers }), JSON.stringify(alerts)).toEqual(new Map([
        [
          'frontend/bun.lock',
          new Set(),
        ],
      ]))
    }
    expect(asked).toEqual([])
  })
  it('warns and exempts nothing when the advisory read of a changed package fails', async () => {
    const warnings = []
    const exempt = await securityExemptions([
      changed('frontend/bun.lock', 'npm', 'bun', bunLock, ['fixed-pkg@1.2.0'], ['fixed-pkg@1.2.3']),
    ], {
      alerts: alertsOn('frontend/bun.lock', 'npm', 'fixed-pkg'),
      readers: {
        advisories: async () => {
          throw new Error('Advisory read failed: 503')
        },
      },
      warn: message => warnings.push(message),
    })
    expect(exempt).toEqual(new Map([
      [
        'frontend/bun.lock',
        new Set(),
      ],
    ]))
    expect(warnings).toEqual(['Skipped the security fixes of fixed-pkg in frontend/bun.lock: Advisory read failed: 503'])
  })
})

describe('timeReaders', () => {
  const registry = () => {
    const urls = []
    return {
      urls,
      request: async (url) => {
        urls.push(url)
        if (url.startsWith('https://registry.npmjs.org/')) {
          return Response.json({
            time: { 'created': oneHour, '1.0.0': fourDays },
          })
        }
        return url.endsWith('/missing')
          ? new Response('', { status: 404 })
          : crateIndexResponse([
              { name: 'foo', vers: '1.0.0', pubtime: fourDays },
              { name: 'foo', vers: '1.1.0' },
            ])
      },
    }
  }
  it('reads each npm packument once for one set and returns the time of a version, or null', async () => {
    const { urls, request } = registry()
    const times = timeReaders({ request })
    expect(await Promise.all([
      times.npmTime('@leapmux-test/times', '1.0.0'),
      times.npmTime('@leapmux-test/times', '2.0.0'),
      times.npmTime('@leapmux-test/times', 'created'),
    ])).toEqual([fourDays, null, null])
    expect(urls).toEqual(['https://registry.npmjs.org/%40leapmux-test%2Ftimes'])
  })
  it('reads each crate index file once for one set, and returns null for a version or a crate that the index lacks', async () => {
    const { urls, request } = registry()
    const times = timeReaders({ request })
    expect(await times.crateTime('foo', '1.0.0')).toBe(fourDays)
    expect(await times.crateTime('foo', '1.1.0')).toBeNull()
    expect(await times.crateTime('foo', '9.9.9')).toBeNull()
    expect(await times.crateTime('missing', '1.0.0')).toBeNull()
    expect(urls).toEqual(['https://index.crates.io/3/f/foo', 'https://index.crates.io/mi/ss/missing'])
  })
  it('keeps one cache for each set, and separates an npm package from a crate with the same name', async () => {
    const { urls, request } = registry()
    const first = timeReaders({ request })
    expect(await first.npmTime('foo', '1.0.0')).toBe(fourDays)
    expect(await first.crateTime('foo', '1.0.0')).toBe(fourDays)
    expect(await timeReaders({ request }).npmTime('foo', '1.0.0')).toBe(fourDays)
    expect(urls).toEqual(['https://registry.npmjs.org/foo', 'https://index.crates.io/3/f/foo', 'https://registry.npmjs.org/foo'])
    expect(first.goTimes).toBe(readGoTimes)
  })
  it('fails when a registry answers with an error', async () => {
    const failing = status => timeReaders({
      request: async () => new Response('', { status }),
    })
    await expect(failing(500).npmTime('leapmux-test-error', '1.0.0')).rejects.toThrow('npm packument read failed: 500')
    await expect(failing(503).crateTime('foo', '1.0.0')).rejects.toThrow('Crate index read failed: 503')
  })
})

describe('readGoTimes', () => {
  it('reads the times of published versions through GOPROXY and leaves out an unknown version', async () => {
    const checkout = scratch.create('check-dependency-age-go')
    // The module proxy layout. go list -m reads only the .info and .mod files.
    const proxy = resolve(checkout, 'proxy')
    mkdirSync(resolve(proxy, 'example.com/lib/@v'), { recursive: true })
    writeFileSync(resolve(proxy, 'example.com/lib/@v/v1.2.3.mod'), 'module example.com/lib\n')
    writeFileSync(resolve(proxy, 'example.com/lib/@v/v1.2.3.info'), JSON.stringify({ Version: 'v1.2.3', Time: '2026-10-20T00:00:00Z' }))
    const env = {
      ...process.env,
      GOPROXY: pathToFileURL(proxy).href,
      GOSUMDB: 'off',
      GONOPROXY: '',
      GONOSUMDB: '',
      GOPRIVATE: '',
      GOFLAGS: '-modcacherw',
      GOMODCACHE: resolve(checkout, 'module-cache'),
    }
    expect(await readGoTimes(['example.com/lib@v1.2.3', 'example.com/lib@v1.2.4'], { checkout, env })).toEqual(new Map([
      ['example.com/lib@v1.2.3', '2026-10-20T00:00:00Z'],
    ]))
  })
  it('runs no go command for an empty list', async () => {
    expect(await readGoTimes([], { checkout: '/nonexistent' })).toEqual(new Map())
  })
})

describe('main', () => {
  const capture = () => {
    const lines = []
    return {
      lines,
      write: (text) => {
        lines.push(text)
      },
    }
  }
  const lockfiles = () => repository({
    'frontend/bun.lock': bunLock(['a@1.0.0']),
  }, {
    'frontend/bun.lock': bunLock(['a@1.1.0']),
  })
  it('prints an annotation for each violation and returns 1', async () => {
    const { checkout, base } = lockfiles()
    const stdout = capture()
    const stderr = capture()
    expect(await main([
      '--base',
      base,
      '--head-ref',
      'renovate/frontend',
    ], {
      stdout,
      stderr,
      checkout,
      now,
      readers: offlineReaders,
      times: timeTable({ 'a@1.1.0': oneHour }),
    })).toBe(1)
    expect(stdout.lines).toEqual(['::error file=frontend/bun.lock::a@1.1.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days ago.\n'])
    expect(stderr.lines).toEqual([])
  })
  it('returns 0 when every new version is old enough', async () => {
    const { checkout, base } = lockfiles()
    const stdout = capture()
    expect(await main([
      '--base',
      base,
      '--head-ref',
      'renovate/frontend',
    ], {
      stdout,
      stderr: capture(),
      checkout,
      now,
      readers: offlineReaders,
      times: timeTable({ 'a@1.1.0': fourDays }),
    })).toBe(0)
    expect(stdout.lines).toEqual(['Every version that the lockfiles add meets the minimum release age.\n'])
  })
  it('reads the head lockfiles from the revision that --head gives, and prints a skipped fix as a warning', async () => {
    const { checkout, base, head } = lockfiles()
    git(checkout, ['checkout', '-q', '--detach', base])
    const stdout = capture()
    expect(await main([
      '--base',
      base,
      '--head',
      head,
      '--head-ref',
      'renovate/security',
    ], {
      stdout,
      stderr: capture(),
      checkout,
      now,
      readers: {
        ...offlineReaders,
        advisories: async () => {
          throw new Error('Advisory read failed: 503\nretry later')
        },
      },
      openAlerts: async () => alertsOn('frontend/bun.lock', 'npm', 'a'),
      times: timeTable({ 'a@1.1.0': oneHour }),
    })).toBe(1)
    expect(stdout.lines).toEqual([
      '::warning::Skipped the security fixes of a in frontend/bun.lock: Advisory read failed: 503%0Aretry later\n',
      '::error file=frontend/bun.lock::a@1.1.0 was published at 2026-10-30T23:00:00.000Z, less than 3 days ago.\n',
    ])
  })
  it('returns 1 with the reason when the check cannot read the base revision', async () => {
    const { checkout } = lockfiles()
    const stderr = capture()
    expect(await main([
      '--base',
      'missing-revision',
      '--head-ref',
      'renovate/frontend',
    ], {
      stdout: capture(),
      stderr,
      checkout,
      now,
      readers: offlineReaders,
      times: offlineTimes,
    })).toBe(1)
    expect(stderr.lines.join('')).toContain('Cannot read the git revision missing-revision')
  })
})

describe('SECURITY_BRANCHES', () => {
  it('lists two security branches', () => {
    expect(SECURITY_BRANCHES).toEqual(['renovate/security', 'renovate/go-security'])
  })
})

describe('parseJsonStream', () => {
  it('splits concatenated objects, including braces and quotes inside strings', () => {
    expect(parseJsonStream('{"Path":"a","Err":"x { \\" }"}\n{\n "Path": "b"\n}\n')).toEqual([
      { Path: 'a', Err: 'x { " }' },
      { Path: 'b' },
    ])
    expect(parseJsonStream('')).toEqual([])
    expect(() => parseJsonStream('{"Path":')).toThrow('truncated')
  })
})

describe('annotations', () => {
  it('escapes the workflow command characters', () => {
    expect(annotations([
      { file: 'dir,a:b.lock', message: 'line 100%\nnext' },
    ])).toEqual(['::error file=dir%2Ca%3Ab.lock::line 100%25%0Anext'])
  })
})

describe('the check-dependency-age entry point', () => {
  const cli = (checkout, args) => spawnSync(process.execPath, [
    resolve(root, 'scripts/check-dependency-age.mjs'),
    ...args,
  ], { cwd: checkout, encoding: 'utf8' })
  it('checks a branch that a person owns, and succeeds when no lockfile changes', () => {
    // The entry point checks the repository that holds it. HEAD against itself changes nothing.
    const result = cli(root, [
      '--base',
      'HEAD',
      '--head-ref',
      'feature/x',
    ])
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toBe('Every version that the lockfiles add meets the minimum release age.\n')
  })
  it('rejects missing arguments', () => {
    const result = cli(root, ['--head-ref', 'renovate/x'])
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Usage')
  })
})
