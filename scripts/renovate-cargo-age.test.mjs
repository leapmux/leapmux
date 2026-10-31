import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { CargoAgeError, cargoAgePolicy, cargoPullers, checkCargoLock, crateReleases, selectiveSpecs, updateWithAge } from './renovate-cargo-age.mjs'
import { agedIndexArgs } from './renovate-crate-index-proxy.mjs'
import { lockedVersions } from './renovate-lockfile-versions.mjs'
import { crateIndexResponse, scratchDirectories } from './test-support.mjs'

const scratch = scratchDirectories()
afterEach(scratch.cleanup)

const old = '2026-10-20T00:00:00Z'
const young = '2026-10-30T00:00:00Z'
const cutoff = '2026-10-28T00:00:00Z'
// The cargo options of the fake aged index that updateWithAge starts in the tests.
const indexUrl = 'sparse+http://127.0.0.1:1/'
const cargoArgs = agedIndexArgs(indexUrl)

// Each package is `[name, version]`, or `[name, version, dependencies]`.
function cargoLock(packages) {
  return `version = 4\n\n${packages.map(([name, version, dependencies]) => `[[package]]\nname = "${name}"\nversion = "${version}"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n${dependencies
    ? `dependencies = [${dependencies.map(dependency => JSON.stringify(dependency)).join(', ')}]\n`
    : ''}`).join('\n')}`
}
const lockOf = (...packages) => cargoLock(packages)

// A git that locks `base` at HEAD of a repository whose Cargo.lock lies under desktop/rust/.
function fakeGit(base) {
  const calls = []
  const git = (args) => {
    calls.push(args.join(' '))
    if (args[0] === 'rev-parse' && args[1] === '--show-prefix')
      return { status: 0, stdout: 'desktop/rust/\n', stderr: '' }
    if (args[0] === 'cat-file') {
      return {
        status: base == null
          ? 1
          : 0,
        stdout: '',
        stderr: '',
      }
    }
    if (args[0] === 'rev-parse')
      return { status: 0, stdout: 'abc\n', stderr: '' }
    return {
      status: 0,
      stdout: cargoLock(base),
      stderr: '',
    }
  }
  return { git, calls }
}

// The index entries of crates, from `[version, publishedAt]` pairs.
function releaseTable(releases) {
  const reads = []
  return {
    reads,
    releases: async (name) => {
      reads.push(name)
      return (releases[name] ?? []).map(([version, publishedAt]) => ({ version, publishedAt, yanked: false }))
    },
  }
}

describe('cargoAgePolicy', () => {
  const lockfile = '/workspace/desktop/rust/Cargo.lock'
  const metadata = declared => ({
    status: 0,
    stdout: JSON.stringify({
      packages: [
        {
          dependencies: declared.map(([name, req]) => ({ name, req, source: 'registry+https://github.com/rust-lang/crates.io-index' })),
        },
      ],
    }),
    stderr: '',
  })
  const policy = authorized => ({
    cutoff,
    authorized: { 'desktop/rust/Cargo.lock': authorized },
  })
  it('allows the base versions, the versions before the call, and the closure of each active root', () => {
    const { git } = fakeGit([
      ['foo', '1.0.0'],
    ])
    const commands = []
    const age = cargoAgePolicy({
      run: (args) => {
        commands.push(args[0])
        return metadata([
          ['declared', '=2.0.0'],
        ])
      },
      git,
      policy: policy({
        'fix@2.0.1': ['pin@1.0.5'],
        'declared@2.0.0': [],
        'held@3.0.0': ['held-pin@1.0.0'],
        'idle@4.0.0': ['idle-pin@1.0.0'],
      }),
      preciseTarget: 'fix@2.0.1',
      lockfile,
      previous: lockOf(['foo', '1.0.0'], ['held', '3.0.0'], ['young', '9.0.0']),
    })
    expect(age.cutoff).toBe(Date.parse(cutoff))
    expect(age.baseVersions).toEqual(lockedVersions('cargo', lockOf(['foo', '1.0.0'])))
    expect(age.activeRoots).toEqual(['declared@2.0.0', 'fix@2.0.1', 'held@3.0.0'])
    expect([...age.exempt].sort()).toEqual([
      'declared@2.0.0',
      'fix@2.0.1',
      'held-pin@1.0.0',
      'held@3.0.0',
      'pin@1.0.5',
    ])
    expect([...age.allowed].sort()).toEqual([
      'declared@2.0.0',
      'fix@2.0.1',
      'foo@1.0.0',
      'held-pin@1.0.0',
      'held@3.0.0',
      'pin@1.0.5',
      'young@9.0.0',
    ])
    // The manifests are read once, for the roots that no cheaper rule activates.
    expect(commands).toEqual(['metadata'])
  })
  it('takes the roots of its own lockfile only, and never activates a root that the base locks', () => {
    const { git } = fakeGit([
      ['fix', '2.0.1'],
    ])
    const run = () => {
      throw new Error('No root needs the manifests.')
    }
    expect(cargoAgePolicy({
      run,
      git,
      policy: {
        cutoff,
        authorized: {
          'desktop/rust/Cargo.lock': {
            'fix@2.0.1': ['pin@1.0.5'],
          },
          'frontend/bun.lock': { 'other@1.0.0': [] },
        },
      },
      preciseTarget: 'fix@2.0.1',
      lockfile,
    })).toMatchObject({
      activeRoots: [],
      exempt: new Set(),
    })
    expect(cargoAgePolicy({
      run,
      git,
      policy: { cutoff, authorized: {} },
      lockfile,
    }).allowed).toEqual(new Set(['fix@2.0.1']))
  })
  it('fails when cargo cannot read the workspace manifests that a root needs', () => {
    const { git } = fakeGit([])
    expect(() => cargoAgePolicy({
      run: () => ({ status: 101, stdout: '', stderr: 'error: failed to parse manifest\n' }),
      git,
      policy: policy({ 'declared@2.0.0': [] }),
      lockfile,
    })).toThrow('cargo metadata failed: error: failed to parse manifest')
  })
  it('treats an absent base lockfile as locking nothing, and requires a cutoff', () => {
    const { git } = fakeGit(null)
    expect(cargoAgePolicy({
      run: () => metadata([]),
      git,
      policy: policy({}),
      lockfile,
    }).baseVersions).toEqual(new Map())
    expect(() => cargoAgePolicy({
      run: () => metadata([]),
      git,
      policy: { authorized: {} },
      lockfile,
    })).toThrow('requires a publication cutoff')
  })
  it('fails when the lockfile lies outside a repository', () => {
    const git = args => args[0] === 'rev-parse' && args[1] === '--show-prefix'
      ? { status: 128, stdout: '', stderr: 'fatal: not a git repository' }
      : fakeGit([]).git(args)
    expect(() => cargoAgePolicy({
      run: () => metadata([]),
      git,
      policy: policy({}),
      lockfile,
    })).toThrow('Cannot find the repository of /workspace/desktop/rust/Cargo.lock: fatal: not a git repository')
  })
})

describe('checkCargoLock', () => {
  const age = (base, exempt = []) => ({
    cutoff: Date.parse(cutoff),
    baseVersions: lockedVersions('cargo', cargoLock(base)),
    exempt: new Set(exempt),
  })
  it('accepts old-enough and exempt new versions, and reads each new crate once', async () => {
    const table = releaseTable({
      foo: [
        ['1.4.1', old],
        ['2.0.0', old],
      ],
    })
    await checkCargoLock(lockOf(['foo', '1.4.1'], ['foo', '2.0.0'], ['fix', '2.0.1'], ['kept', '1.0.0']), age([
      ['kept', '1.0.0'],
    ], ['fix@2.0.1']), table.releases)
    expect(table.reads).toEqual(['foo'])
  })
  it('reads nothing for a lockfile that adds no version', async () => {
    const table = releaseTable({})
    await checkCargoLock(lockOf(['kept', '1.0.0']), age([
      ['kept', '1.0.0'],
    ]), table.releases)
    expect(table.reads).toEqual([])
  })
  it('rejects a young version and a version without a publication time, with each listed', async () => {
    const { releases } = releaseTable({
      foo: [
        ['1.4.2', young],
      ],
    })
    const error = await checkCargoLock(lockOf(['foo', '1.4.2'], ['bar', '1.0.0']), age([]), releases).catch(caught => caught)
    expect(error).toBeInstanceOf(CargoAgeError)
    expect(error.reason).toBe('a version is younger than the release age')
    expect(error.violations.map(violation => violation.name)).toEqual(['bar', 'foo'])
    expect(error.message).toBe('Cargo release-age check failed: a version is younger than the release age\n- bar@1.0.0 (no release timestamp)\n- foo@1.4.2 (published 2026-10-30T00:00:00Z)')
  })
  it('rejects a version that replaces a higher base version of its line before any read', async () => {
    const releases = () => {
      throw new Error('A downgrade must fail before any read.')
    }
    const error = await checkCargoLock(lockOf(['foo', '1.4.1']), age([
      ['foo', '1.4.2'],
    ]), releases).catch(caught => caught)
    expect(error.reason).toBe('a lower version replaces a locked version')
    expect(error.message).toContain('- cargo would downgrade foo from 1.4.2 to 1.4.1')
    expect(error.violations).toEqual([
      expect.objectContaining({ id: 'foo@1.4.1', name: 'foo', version: '1.4.1' }),
    ])
  })
})

describe('crateReleases', () => {
  it('reads the sparse index once for each crate', async () => {
    const urls = []
    const releases = crateReleases({
      index: 'http://index.test',
      request: async (url) => {
        urls.push(url)
        return crateIndexResponse([
          { name: 'foo', vers: '1.0.0', pubtime: old },
        ])
      },
    })
    expect(await releases('foo')).toEqual([
      { version: '1.0.0', publishedAt: old, yanked: false },
    ])
    await releases('foo')
    expect(urls).toEqual(['http://index.test/3/f/foo'])
  })
})

describe('updateWithAge', () => {
  // A workspace whose base commit locks `before`, whose update writes `updated`, and whose
  // update of specs writes what `select(specs)` returns, or fails with the status it returns.
  function workspace({ before, base = before, updated, releases = {}, status = 0, select, fullUpdate = false, authorized = {} }) {
    const directory = scratch.create('cargo-age')
    const lockfile = join(directory, 'Cargo.lock')
    if (before !== null)
      writeFileSync(lockfile, cargoLock(before))
    const commands = []
    const selections = []
    const indexes = []
    const options = {
      run: (args) => {
        commands.push(args.find(arg => !cargoArgs.includes(arg)))
        if (args[0] === 'locate-project')
          return { status: 0, stdout: `${join(directory, 'Cargo.toml')}\n`, stderr: '' }
        if (args[0] === 'metadata') {
          return {
            status: 0,
            stdout: JSON.stringify({ packages: [] }),
            stderr: '',
          }
        }
        expect(args.slice(0, cargoArgs.length + 1)).toEqual([...cargoArgs, 'update'])
        const specs = args.slice(cargoArgs.length + 1)
        selections.push(specs)
        if (!select)
          return { status: 101, stdout: '', stderr: 'unexpected cargo command' }
        const selected = select(specs)
        if (!Array.isArray(selected))
          return { status: selected.status, stdout: '', stderr: selected.stderr }
        writeFileSync(lockfile, cargoLock(selected))
        return { status: 0, stdout: '', stderr: '' }
      },
      update: async (args) => {
        commands.push('update')
        expect(args).toEqual(cargoArgs)
        if (status === 0)
          writeFileSync(lockfile, cargoLock(updated))
        return { code: status, signal: null }
      },
      git: fakeGit(base).git,
      releases: releaseTable(releases).releases,
      startIndex: async (options) => {
        const index = { ...options, closed: false }
        indexes.push(index)
        return {
          url: indexUrl,
          close: async () => {
            index.closed = true
          },
        }
      },
      policy: {
        cutoff,
        cratesIndex: 'http://index.test',
        authorized: { 'desktop/rust/Cargo.lock': authorized },
      },
      fullUpdate,
    }
    return {
      lockfile,
      commands,
      selections,
      indexes,
      options,
      locked: () => readFileSync(lockfile, 'utf8'),
    }
  }
  it('runs the update through an aged index of the base versions and the exemptions, and closes the index', async () => {
    const { lockfile, indexes, options, locked } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      updated: [
        ['foo', '1.4.1'],
        ['fix', '2.0.1'],
      ],
      releases: {
        foo: [
          ['1.4.1', old],
        ],
      },
      authorized: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
    })
    expect(await updateWithAge({ ...options, preciseTarget: 'fix@2.0.1' })).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      activeRoots: ['fix@2.0.1'],
    })
    expect(indexes).toEqual([
      {
        upstream: 'http://index.test',
        cutoffMs: Date.parse(cutoff),
        allowed: new Set(['foo@1.4.0', 'fix@2.0.1', 'pin@1.0.5']),
        closed: true,
      },
    ])
    expect(locked()).toBe(lockOf(['foo', '1.4.1'], ['fix', '2.0.1']))
  })
  it('keeps the lockfile of an update that passes the check', async () => {
    const { lockfile, indexes, options, locked } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      updated: [
        ['foo', '1.4.1'],
      ],
      releases: {
        foo: [
          ['1.4.1', old],
        ],
      },
    })
    expect(await updateWithAge(options)).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      activeRoots: [],
    })
    expect(locked()).toBe(lockOf(['foo', '1.4.1']))
    expect(indexes.map(index => index.closed)).toEqual([true])
  })
  it('restores the lockfile of a failed check to its state before the update', async () => {
    const { options, locked } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      updated: [
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
      },
    })
    const error = await updateWithAge(options).catch(caught => caught)
    expect(error).toBeInstanceOf(CargoAgeError)
    expect(error.message).toEndWith('- foo@1.4.2 (published 2026-10-30T00:00:00Z)\nCargo.lock was restored to its state before this command.')
    expect(locked()).toBe(lockOf(['foo', '1.4.0']))
  })
  it('restores the lockfile when the update forces a downgrade', async () => {
    const { options, locked } = workspace({
      before: [
        ['foo', '1.4.2'],
      ],
      updated: [
        ['foo', '1.4.1'],
      ],
    })
    expect((await updateWithAge(options).catch(caught => caught)).message).toContain('cargo would downgrade foo from 1.4.2 to 1.4.1')
    expect(locked()).toBe(lockOf(['foo', '1.4.2']))
  })
  it('removes a lockfile that did not exist before a failed check, and selects no crates for it', async () => {
    const { lockfile, commands, options } = workspace({
      before: null,
      base: null,
      updated: [
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
      },
      fullUpdate: true,
    })
    expect((await updateWithAge(options).catch(caught => caught)).message).toEndWith('The new Cargo.lock was removed.')
    expect(existsSync(lockfile)).toBe(false)
    expect(commands).toEqual(['locate-project', 'update'])
  })
  it('checks nothing after a failed update, and still closes the index', async () => {
    const { commands, indexes, options, locked } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      updated: [],
      status: 101,
    })
    expect(await updateWithAge(options)).toEqual({
      result: { code: 101, signal: null },
      activeRoots: [],
    })
    expect(commands).toEqual(['locate-project', 'update'])
    expect(indexes.map(index => index.closed)).toEqual([true])
    expect(locked()).toBe(lockOf(['foo', '1.4.0']))
  })
  it('closes the index when the update throws', async () => {
    const { indexes, options } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      updated: [],
    })
    await expect(updateWithAge({
      ...options,
      update: async () => {
        throw new Error('The shim received SIGTERM.')
      },
    })).rejects.toThrow('The shim received SIGTERM.')
    expect(indexes.map(index => index.closed)).toEqual([true])
  })
  const family = {
    before: [
      [
        'futures',
        '0.4.79',
        ['js'],
      ],
      ['js', '0.3.106'],
      ['other', '1.0.0'],
      [
        'sys',
        '0.3.106',
        ['js'],
      ],
    ],
    updated: [
      [
        'futures',
        '0.4.80',
        ['js'],
      ],
      ['js', '0.3.107'],
      ['other', '1.1.0'],
      [
        'sys',
        '0.3.107',
        ['js'],
      ],
    ],
    releases: {
      futures: [
        ['0.4.80', young],
      ],
      js: [
        ['0.3.107', young],
      ],
      sys: [
        ['0.3.107', young],
      ],
      other: [
        ['1.1.0', old],
      ],
    },
  }
  const familyKept = [
    [
      'futures',
      '0.4.79',
      ['js'],
    ],
    ['js', '0.3.106'],
    ['other', '1.1.0'],
    [
      'sys',
      '0.3.106',
      ['js'],
    ],
  ]
  it('keeps the crates of a failed check at their versions and updates every other crate of a full update', async () => {
    const { lockfile, selections, options, locked } = workspace({
      ...family,
      select: () => familyKept,
      fullUpdate: true,
    })
    expect(await updateWithAge(options)).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      activeRoots: [],
      excluded: ['futures', 'js', 'sys'],
    })
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
    expect(locked()).toBe(cargoLock(familyKept))
  })
  it('fails and restores the lockfile when cargo rejects the selective update', async () => {
    const { selections, options, locked } = workspace({
      ...family,
      select: () => ({ status: 101, stderr: 'error: failed to select a version for `other`' }),
      fullUpdate: true,
    })
    const error = await updateWithAge(options).catch(caught => caught)
    expect(error.reason).toBe('cargo rejected the selective update')
    expect(error.message).toContain('error: failed to select a version for `other`')
    expect(error.message).toEndWith('Cargo.lock was restored to its state before this command.')
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
    expect(locked()).toBe(cargoLock(family.before))
  })
  it('fails a targeted update whose check fails, and never selects crates for it', async () => {
    const { selections, options, locked } = workspace({
      ...family,
      select: () => familyKept,
    })
    expect((await updateWithAge(options).catch(caught => caught)).reason).toBe('a version is younger than the release age')
    expect(selections).toEqual([])
    expect(locked()).toBe(cargoLock(family.before))
  })
  it('leaves out a selected crate that pulls a young crate back through a crate that it adds', async () => {
    const pulled = [
      ['foo', '1.4.2'],
      [
        'mid',
        '1.0.0',
        ['foo'],
      ],
      ['other', '1.1.0'],
      [
        'parent',
        '1.1.0',
        ['mid'],
      ],
    ]
    const kept = [
      ['foo', '1.4.1'],
      ['other', '1.1.0'],
      ['parent', '1.0.0'],
    ]
    const { selections, options, locked } = workspace({
      before: [
        ['foo', '1.4.1'],
        ['other', '1.0.0'],
        ['parent', '1.0.0'],
      ],
      updated: pulled,
      releases: {
        foo: [
          ['1.4.2', young],
        ],
        mid: [
          ['1.0.0', old],
        ],
        other: [
          ['1.1.0', old],
        ],
        parent: [
          ['1.1.0', old],
        ],
      },
      select: specs => specs.includes('parent@1.0.0')
        ? pulled
        : kept,
      fullUpdate: true,
    })
    expect(await updateWithAge(options)).toMatchObject({
      excluded: ['foo', 'parent'],
    })
    expect(selections).toEqual([
      ['other@1.0.0', 'parent@1.0.0'],
      ['other@1.0.0'],
    ])
    expect(locked()).toBe(cargoLock(kept))
  })
  it('repeats the selection without a selected crate that still pulls the young version', async () => {
    // The first round leaves out only foo. The selected parent pulls the young foo back,
    // so the second round leaves out parent too.
    const { selections, options } = workspace({
      before: [
        ['foo', '1.4.1'],
        ['other', '1.0.0'],
        ['parent', '1.0.0'],
      ],
      updated: [
        ['foo', '1.4.2'],
        ['other', '1.1.0'],
        ['parent', '1.1.0'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
        other: [
          ['1.1.0', old],
        ],
        parent: [
          ['1.1.0', old],
        ],
      },
      select: specs => specs.includes('parent@1.0.0')
        ? [
            ['foo', '1.4.2'],
            ['other', '1.1.0'],
            [
              'parent',
              '1.1.0',
              ['foo'],
            ],
          ]
        : [
            ['foo', '1.4.1'],
            ['other', '1.1.0'],
            [
              'parent',
              '1.0.0',
              ['foo'],
            ],
          ],
      fullUpdate: true,
    })
    expect(await updateWithAge(options)).toMatchObject({
      excluded: ['foo', 'parent'],
    })
    expect(selections).toEqual([
      ['other@1.0.0', 'parent@1.0.0'],
      ['other@1.0.0'],
    ])
  })
  it('fails with the check error when no exclusion removes a crate from the selection', async () => {
    // A young new crate that only the workspace requires has no registry dependent to exclude.
    const { selections, options, locked } = workspace({
      before: [
        ['other', '1.0.0'],
      ],
      updated: [
        ['newdep', '1.0.0'],
        ['other', '1.0.0'],
      ],
      releases: {
        newdep: [
          ['1.0.0', young],
        ],
      },
      select: () => {
        throw new Error('Unexpected selective update.')
      },
      fullUpdate: true,
    })
    expect((await updateWithAge(options).catch(caught => caught)).reason).toBe('a version is younger than the release age')
    expect(selections).toEqual([])
    expect(locked()).toBe(lockOf(['other', '1.0.0']))
  })
  it('keeps a locked version that a full update lowers, as for a yanked version, and updates every other crate', async () => {
    const kept = [
      ['foo', '1.4.2'],
      ['other', '1.1.0'],
    ]
    const { lockfile, selections, options, locked } = workspace({
      before: [
        ['foo', '1.4.2'],
        ['other', '1.0.0'],
      ],
      // Cargo never selects a yanked version when it unlocks every crate.
      updated: [
        ['foo', '1.4.1'],
        ['other', '1.1.0'],
      ],
      releases: {
        other: [
          ['1.1.0', old],
        ],
      },
      select: () => kept,
      fullUpdate: true,
    })
    expect(await updateWithAge(options)).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      activeRoots: [],
      excluded: ['foo'],
    })
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
    expect(locked()).toBe(lockOf(...kept))
  })
})

describe('cargoPullers', () => {
  const before = cargoLock([
    ['js', '0.3.106'],
    [
      'wrapper',
      '1.0.0',
      ['js'],
    ],
    [
      'top',
      '1.0.0',
      ['wrapper'],
    ],
    ['reqwest', '0.13.4'],
  ])
  const text = `${cargoLock([
    ['js', '0.3.107'],
    [
      'sys',
      '0.3.107',
      ['js'],
    ],
    [
      'futures',
      '0.4.80',
      ['js', 'sys 0.3.107'],
    ],
    [
      'reqwest',
      '0.13.5',
      ['futures'],
    ],
    [
      'wrapper',
      '1.0.0',
      ['js'],
    ],
    [
      'top',
      '1.1.0',
      ['wrapper'],
    ],
  ])}\n[[package]]\nname = "app"\nversion = "0.1.0"\ndependencies = ["reqwest", "top"]\n`
  it('finds each selected crate above a blocked crate through the crates that the update adds', () => {
    // sys and futures are new, so the search passes through them to the selected reqwest.
    expect([
      ...cargoPullers(text, ['js'], {
        selected: new Set(['reqwest', 'top']),
        before,
      }),
    ]).toEqual(['reqwest'])
  })
  it('stops at a crate that the base locks unchanged, and at the first selected crate', () => {
    // wrapper is unchanged, so top cannot pull js through it.
    expect([
      ...cargoPullers(text, ['js'], {
        selected: new Set(['top']),
        before,
      }),
    ]).toEqual([])
    // futures is selected, so the search never reaches reqwest above it.
    expect([
      ...cargoPullers(text, ['js'], {
        selected: new Set(['futures', 'reqwest']),
        before,
      }),
    ]).toEqual(['futures'])
    expect([
      ...cargoPullers(text, [], {
        selected: new Set(['reqwest']),
        before,
      }),
    ]).toEqual([])
  })
})

describe('selectiveSpecs', () => {
  it('lists each replaced version of every crate that is not excluded', () => {
    const before = cargoLock([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.0'],
      ['foo', '1.0.0'],
      ['kept', '1.0.0'],
    ])
    const updated = cargoLock([
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
      ['foo', '1.1.0'],
      ['kept', '1.0.0'],
      ['added', '1.0.0'],
    ])
    expect(selectiveSpecs(before, updated, new Set())).toEqual(['bitflags@2.13.0', 'foo@1.0.0'])
    expect(selectiveSpecs(before, updated, new Set(['foo']))).toEqual(['bitflags@2.13.0'])
    expect(selectiveSpecs(before, before, new Set())).toEqual([])
  })
})
