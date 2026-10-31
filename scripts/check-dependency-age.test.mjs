import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it } from 'bun:test'
import { annotations, checkDependencyAge, main, parseJsonStream, readCrateTime, readGoTimes, readNpmTime, SECURITY_BRANCHES } from './check-dependency-age.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

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

function git(directory, args) {
  const result = spawnSync('git', args, {
    cwd: directory,
    encoding: 'utf8',
    env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1' },
  })
  expect(result.status, `git ${args.join(' ')}: ${result.stderr}`).toBe(0)
  return result.stdout.trim()
}

function write(directory, files) {
  for (const [file, content] of Object.entries(files)) {
    if (content === null) {
      rmSync(resolve(directory, file), { force: true })
      continue
    }
    mkdirSync(resolve(directory, file, '..'), { recursive: true })
    writeFileSync(resolve(directory, file), content)
  }
}

// A base commit, and a working tree that holds the head of the Renovate PR.
function repository(base, head) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const checkout = mkdtempSync(resolve(root, '.tmp/check-dependency-age-'))
  fixtures.push(checkout)
  write(checkout, {
    'renovate.json': readFileSync(resolve(root, 'renovate.json')),
    ...base,
  })
  for (const args of [
    ['init', '-q', '-b', 'main'],
    ['config', 'core.fsmonitor', 'false'],
    ['config', 'gc.auto', '0'],
    ['config', 'maintenance.auto', 'false'],
    ['config', 'user.email', 'fixture@example.com'],
    ['config', 'user.name', 'Fixture'],
    ['config', 'commit.gpgsign', 'false'],
    ['add', '-A'],
    ['commit', '-q', '-m', 'base'],
  ])
    git(checkout, args)
  const sha = git(checkout, ['rev-parse', 'HEAD'])
  write(checkout, head)
  return { checkout, base: sha }
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
  it('skips a branch that Renovate does not own without reading a file or a registry', async () => {
    for (const headRef of ['feature/age', '', undefined, 'main']) {
      expect(await checkDependencyAge({
        base: 'missing',
        headRef,
        checkout: '/nonexistent',
        readers: offlineReaders,
        times: offlineTimes,
      })).toBeNull()
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
    const check = async (base, head, readers = advisoryReaders(fix, { pins }), table = {}) => {
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
        times,
      })
      return {
        identities: violations.map(violation => violation.identity),
        reads: times.reads,
      }
    }
    it('accepts the exact patched root and its exact pins without reading their times', async () => {
      expect(await check(['fixed-pkg@1.2.0', 'fixed-pkg-core@1.2.0'], ['fixed-pkg@1.2.3', 'fixed-pkg-core@1.2.3'])).toEqual({ identities: [], reads: [] })
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
        reads: ['pkg@1.2.6'],
      })
    })
  })

  describe('on renovate/go-security', () => {
    const check = async (base, head, advisories, go, table = {}) => {
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
      ], { 'example.com/lib/v2@v2.0.0': 'found' })).toEqual({ identities: [], reads: [] })
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
    it('still rejects another young module in the same PR', async () => {
      expect(await check(['example.com/lib@v1.9.0'], ['example.com/lib/v2@v2.0.0', 'example.com/other@v0.2.0'], [
        advisory('go', 'example.com/lib', '< 2.0.0', '2.0.0'),
      ], { 'example.com/lib/v2@v2.0.0': 'found' }, { 'example.com/other@v0.2.0': oneHour })).toEqual({
        identities: ['example.com/other@v0.2.0'],
        reads: ['example.com/other@v0.2.0'],
      })
    })
  })
})

describe('readNpmTime', () => {
  it('reads each packument once and returns the publication time of a version, or null', async () => {
    const urls = []
    const request = async (url) => {
      urls.push(url)
      return Response.json({
        time: { '1.0.0': fourDays },
      })
    }
    expect(await readNpmTime('@leapmux-test/times', '1.0.0', { request })).toBe(fourDays)
    expect(await readNpmTime('@leapmux-test/times', '2.0.0', { request })).toBeNull()
    expect(urls).toEqual(['https://registry.npmjs.org/%40leapmux-test%2Ftimes'])
  })
  it('fails when the registry answers with an error', async () => {
    await expect(readNpmTime('leapmux-test-error', '1.0.0', {
      request: async () => new Response('', { status: 500 }),
    })).rejects.toThrow('npm packument read failed: 500')
  })
})

describe('readCrateTime', () => {
  it('returns the creation time of a crate version, and null for a version that crates.io lacks', async () => {
    const urls = []
    const request = async (url) => {
      urls.push(url)
      return url.endsWith('/1.0.0')
        ? Response.json({
            version: { created_at: fourDays },
          })
        : new Response('', { status: 404 })
    }
    expect(await readCrateTime('foo', '1.0.0', { request })).toBe(fourDays)
    expect(await readCrateTime('foo', '9.9.9', { request })).toBeNull()
    expect(urls).toEqual(['https://crates.io/api/v1/crates/foo/1.0.0', 'https://crates.io/api/v1/crates/foo/9.9.9'])
  })
  it('fails when crates.io answers with a server error', async () => {
    await expect(readCrateTime('foo', '1.0.0', {
      request: async () => new Response('', { status: 503 }),
    })).rejects.toThrow('Crate version read failed: 503')
  })
})

describe('readGoTimes', () => {
  it('reads the times of published versions through GOPROXY and leaves out an unknown version', async () => {
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    const checkout = mkdtempSync(resolve(root, '.tmp/check-dependency-age-go-'))
    fixtures.push(checkout)
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
  it('succeeds for a branch that Renovate does not own', () => {
    const result = cli(root, [
      '--base',
      'HEAD',
      '--head-ref',
      'feature/x',
    ])
    expect(result.status, result.stderr).toBe(0)
    expect(result.stdout).toContain('is not a Renovate branch')
  })
  it('rejects missing arguments', () => {
    const result = cli(root, ['--head-ref', 'renovate/x'])
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('Usage')
  })
})
