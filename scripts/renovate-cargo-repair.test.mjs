import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'bun:test'
import { cargoPullers, CargoRepairError, cratesRegistry, repairCargoLock, selectiveSpecs, updateWithRepair } from './renovate-cargo-repair.mjs'

const root = resolve(import.meta.dirname, '..')
const fixtures = []
afterEach(() => {
  for (const fixture of fixtures.splice(0)) rmSync(fixture, { recursive: true, force: true })
})

const old = '2026-10-20T00:00:00Z'
const young = '2026-10-30T00:00:00Z'
const cutoff = '2026-10-28T00:00:00Z'

// Each package is `[name, version]`, or `[name, version, dependencies]`.
function cargoLock(packages) {
  return `version = 4\n\n${packages.map(([name, version, dependencies]) => `[[package]]\nname = "${name}"\nversion = "${version}"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n${dependencies
    ? `dependencies = [${dependencies.map(dependency => JSON.stringify(dependency)).join(', ')}]\n`
    : ''}`).join('\n')}`
}

// A fake cargo whose `update -p name@v --precise w` replaces one locked version.
function harness({ base, head, releases, declared = [], authorized = {}, maxRepairRounds = 10, preciseTarget = null, previous, update }) {
  mkdirSync(resolve(root, '.tmp'), { recursive: true })
  const directory = mkdtempSync(resolve(root, '.tmp/cargo-repair-'))
  fixtures.push(directory)
  let packages = head.map(entry => [...entry])
  const lockfile = join(directory, 'Cargo.lock')
  const save = () => writeFileSync(lockfile, cargoLock(packages))
  save()
  const calls = []
  const registryCalls = []
  const run = (args) => {
    calls.push(args)
    if (args[0] === 'locate-project')
      return { status: 0, stdout: `${join(directory, 'Cargo.toml')}\n`, stderr: '' }
    if (args[0] === 'metadata') {
      return {
        status: 0,
        stdout: JSON.stringify({
          packages: [
            {
              dependencies: declared.map(([name, req]) => ({ name, req, source: 'registry+https://github.com/rust-lang/crates.io-index' })),
            },
          ],
        }),
        stderr: '',
      }
    }
    const spec = args[args.indexOf('-p') + 1]
    const target = args[args.indexOf('--precise') + 1]
    const [name, from] = spec.split('@')
    const result = update?.({ name, from, target, packages }) ?? { status: 0 }
    if (result.status === 0 && !result.unchanged) {
      packages = (result.packages ?? packages.map(([crate, version]) => crate === name && version === from
        ? [crate, target]
        : [crate, version]))
      save()
    }
    return { status: result.status, stdout: '', stderr: result.stderr ?? '' }
  }
  const git = (args) => {
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
  const registry = async (name) => {
    registryCalls.push(name)
    return (releases[name] ?? []).map(([num, created_at, yanked = false]) => ({ num, created_at, yanked }))
  }
  const repair = () => repairCargoLock({
    run,
    git,
    registry,
    policy: {
      cutoff,
      maxRepairRounds,
      authorized: { crates: authorized },
    },
    manifestArgs: [
      '--manifest-path',
      join(directory, 'Cargo.toml'),
    ],
    preciseTarget,
    ...(previous === undefined
      ? {}
      : {
          previous: previous === null
            ? null
            : cargoLock(previous),
        }),
  })
  return {
    repair,
    calls,
    registryCalls,
    locked: () => readFileSync(lockfile, 'utf8'),
    updates: () => calls.filter(args => args[0] === 'update'),
  }
}

const lockOf = (...packages) => cargoLock(packages)

describe('repairCargoLock', () => {
  it('runs no repair and reads no registry for an unchanged lockfile with two lines of one crate', async () => {
    const packages = [
      ['bitflags', '1.3.2'],
      ['bitflags', '2.13.1'],
    ]
    const fixture = harness({ base: packages, head: packages, releases: {} })
    expect(await fixture.repair()).toMatchObject({ repairs: [], activeRoots: [] })
    expect(fixture.calls.map(args => args[0])).toEqual(['locate-project'])
    expect(fixture.registryCalls).toEqual([])
  })
  it('repairs a young version to the newest old-enough version of its line and leaves another line alone', async () => {
    const fixture = harness({
      base: [
        ['foo', '0.9.0'],
        ['foo', '1.3.0'],
      ],
      head: [
        ['foo', '0.9.0'],
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['0.9.0', old],
          ['1.3.0', old],
          ['1.4.1', old],
          ['1.4.2', young],
          ['1.5.0', old, true],
        ],
      },
    })
    expect((await fixture.repair()).repairs).toEqual([
      { from: 'foo@1.4.2', to: 'foo@1.4.1' },
    ])
    expect(fixture.locked()).toBe(lockOf(['foo', '0.9.0'], ['foo', '1.4.1']))
    expect(fixture.updates()[0]).toEqual([
      'update',
      '--manifest-path',
      expect.stringContaining('Cargo.toml'),
      '-p',
      'foo@1.4.2',
      '--precise',
      '1.4.1',
    ])
  })
  it('preserves two unchanged versions of one compatible line', async () => {
    const packages = [
      ['foo', '1.2.0'],
      ['foo', '1.4.2'],
    ]
    const fixture = harness({
      base: packages,
      head: [
        ...packages,
        ['bar', '1.0.0'],
      ],
      releases: {
        bar: [
          ['1.0.0', old],
        ],
      },
    })
    expect((await fixture.repair()).repairs).toEqual([])
    expect(fixture.registryCalls).toEqual(['bar'])
  })
  it('keeps an authorized precise target and its exact pin, and repairs another young version of the pinned crate', async () => {
    const fixture = harness({
      base: [
        ['fix', '2.0.0'],
        ['pin', '0.4.0'],
      ],
      head: [
        ['fix', '2.0.1'],
        ['pin', '1.0.5'],
        ['pin', '0.4.1'],
      ],
      authorized: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
      preciseTarget: 'fix@2.0.1',
      releases: {
        fix: [
          ['2.0.0', old],
          ['2.0.1', young],
        ],
        pin: [
          ['0.4.0', old],
          ['0.4.1', young],
          ['1.0.5', young],
        ],
      },
    })
    expect(await fixture.repair()).toMatchObject({
      repairs: [
        { from: 'pin@0.4.1', to: 'pin@0.4.0' },
      ],
      activeRoots: ['fix@2.0.1'],
    })
    expect(fixture.calls.some(args => args[0] === 'metadata')).toBe(false)
  })
  it('keeps an authorized root that a workspace manifest declares exactly', async () => {
    const fixture = harness({
      base: [
        ['fix', '2.0.0'],
      ],
      head: [
        ['fix', '2.0.1'],
        ['pin', '1.0.5'],
      ],
      authorized: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
      declared: [
        ['fix', '=2.0.1'],
      ],
      releases: {
        fix: [
          ['2.0.1', young],
        ],
        pin: [
          ['1.0.5', young],
        ],
      },
    })
    expect(await fixture.repair()).toMatchObject({
      repairs: [],
      activeRoots: ['fix@2.0.1'],
    })
  })
  it('does not activate a root that a manifest declares with a range', async () => {
    const fixture = harness({
      base: [
        ['fix', '2.0.0'],
      ],
      head: [
        ['fix', '2.0.1'],
      ],
      authorized: { 'fix@2.0.1': [] },
      declared: [
        ['fix', '^2.0.1'],
      ],
      releases: {
        fix: [
          ['2.0.0', old],
          ['2.0.1', young],
        ],
      },
    })
    expect((await fixture.repair()).repairs).toEqual([
      { from: 'fix@2.0.1', to: 'fix@2.0.0' },
    ])
  })
  it('keeps an authorized fix that an earlier call of the branch introduced', async () => {
    // Renovate runs `--precise` for the fix, then `cargo update --workspace`. The second
    // call has no precise target, and a lock-only fix has no exact declaration.
    const packages = [
      ['fix', '2.0.1'],
      ['pin', '1.0.5'],
    ]
    const fixture = harness({
      base: [
        ['fix', '2.0.0'],
      ],
      previous: packages,
      head: packages,
      authorized: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
      releases: {
        fix: [
          ['2.0.0', old],
          ['2.0.1', young],
        ],
        pin: [
          ['1.0.5', young],
        ],
      },
    })
    expect(await fixture.repair()).toMatchObject({
      repairs: [],
      activeRoots: ['fix@2.0.1'],
    })
    expect(fixture.calls.some(args => args[0] === 'metadata')).toBe(false)
  })
  it('does not activate an authorized fix that the lockfile before the call lacks', async () => {
    const fixture = harness({
      base: [
        ['fix', '2.0.0'],
      ],
      previous: [
        ['fix', '2.0.0'],
      ],
      head: [
        ['fix', '2.0.1'],
      ],
      authorized: { 'fix@2.0.1': [] },
      releases: {
        fix: [
          ['2.0.0', old],
          ['2.0.1', young],
        ],
      },
    })
    expect(await fixture.repair()).toMatchObject({
      repairs: [
        { from: 'fix@2.0.1', to: 'fix@2.0.0' },
      ],
      activeRoots: [],
    })
  })
  it('does not let a root that the base already locks exempt a young version that it pins', async () => {
    const fixture = harness({
      base: [
        ['fix', '2.0.1'],
      ],
      head: [
        ['fix', '2.0.1'],
        ['pin', '1.0.5'],
      ],
      authorized: {
        'fix@2.0.1': ['pin@1.0.5'],
      },
      declared: [
        ['fix', '=2.0.1'],
      ],
      preciseTarget: 'fix@2.0.1',
      releases: {
        pin: [
          ['1.0.4', old],
          ['1.0.5', young],
        ],
      },
    })
    expect(await fixture.repair()).toMatchObject({
      repairs: [
        { from: 'pin@1.0.5', to: 'pin@1.0.4' },
      ],
      activeRoots: [],
    })
  })
  it('treats a version without a release timestamp as young', async () => {
    const fixture = harness({
      base: [],
      head: [
        ['foo', '1.0.1'],
      ],
      releases: {
        foo: [
          ['1.0.0', old],
          ['1.0.1', null],
        ],
      },
    })
    expect((await fixture.repair()).repairs).toEqual([
      { from: 'foo@1.0.1', to: 'foo@1.0.0' },
    ])
  })
  it('treats an absent base lockfile as locking nothing', async () => {
    const fixture = harness({
      base: null,
      head: [
        ['foo', '1.0.0'],
      ],
      releases: {
        foo: [
          ['1.0.0', old],
        ],
      },
    })
    expect((await fixture.repair()).repairs).toEqual([])
  })
  describe('failure', () => {
    const failure = async (fixture) => {
      const error = await fixture.repair().catch(caught => caught)
      expect(error).toBeInstanceOf(CargoRepairError)
      return error
    }
    it('reports a crate without an eligible version', async () => {
      const error = await failure(harness({
        base: [],
        head: [
          ['foo', '1.4.2'],
        ],
        releases: {
          foo: [
            ['1.4.2', young],
            ['1.4.1', young],
            ['1.4.0', old, true],
          ],
        },
      }))
      expect(error.reason).toBe('no eligible version')
      expect(error.message).toContain('Cargo release-age repair failed: no eligible version\n- foo@1.4.2 (published 2026-10-30T00:00:00Z)')
    })
    it('reports a precise target that cargo rejects, with the end of its error output', async () => {
      const error = await failure(harness({
        base: [],
        head: [
          ['foo', '1.4.2'],
        ],
        releases: {
          foo: [
            ['1.4.1', old],
            ['1.4.2', young],
          ],
        },
        update: () => ({ status: 101, stderr: `${'x'.repeat(4000)}failed to select a version for the requirement` }),
      }))
      expect(error.reason).toBe('cargo rejected --precise 1.4.1')
      expect(error.message).toContain('failed to select a version for the requirement')
      expect(error.message).not.toContain('x'.repeat(2100))
    })
    it('reports a round that leaves the lockfile unchanged', async () => {
      const error = await failure(harness({
        base: [],
        head: [
          ['foo', '1.4.2'],
        ],
        releases: {
          foo: [
            ['1.4.1', old],
            ['1.4.2', young],
          ],
        },
        update: () => ({ status: 0, unchanged: true }),
      }))
      expect(error.reason).toBe('no progress')
    })
    it('reports a chain of repairs that exceeds the round limit', async () => {
      const error = await failure(harness({
        base: [],
        head: [
          ['foo', '1.4.2'],
        ],
        maxRepairRounds: 1,
        releases: {
          foo: [
            ['1.4.1', old],
            ['1.4.2', young],
          ],
          bar: [
            ['2.0.0', old],
            ['2.0.1', young],
          ],
        },
        update: () => ({
          status: 0,
          packages: [
            ['foo', '1.4.1'],
            ['bar', '2.0.1'],
          ],
        }),
      }))
      expect(error.reason).toBe('round limit reached')
      expect(error.message).toContain('- bar@2.0.1')
    })
  })
  describe('replacement floor', () => {
    it('fails when an old-enough lower version replaces a locked version', async () => {
      const fixture = harness({
        base: [
          ['foo', '1.4.2'],
        ],
        head: [
          ['foo', '1.4.1'],
        ],
        releases: {
          foo: [
            ['1.4.1', old],
            ['1.4.2', old],
          ],
        },
      })
      const error = await fixture.repair().catch(caught => caught)
      expect(error.message).toContain('cargo would downgrade foo from 1.4.2 to 1.4.1')
      expect(fixture.registryCalls).toEqual([])
    })
    it('accepts a pure addition beside a kept version and an upgrade of the lower of two versions', async () => {
      const addition = harness({
        base: [
          ['foo', '1.4.2'],
        ],
        head: [
          ['foo', '1.4.1'],
          ['foo', '1.4.2'],
        ],
        releases: {
          foo: [
            ['1.4.1', old],
          ],
        },
      })
      expect((await addition.repair()).repairs).toEqual([])
      const upgrade = harness({
        base: [
          ['foo', '1.2.0'],
          ['foo', '1.4.2'],
        ],
        head: [
          ['foo', '1.3.0'],
          ['foo', '1.4.2'],
        ],
        releases: {
          foo: [
            ['1.3.0', old],
          ],
        },
      })
      expect((await upgrade.repair()).repairs).toEqual([])
    })
    it('never compares a change in another line', async () => {
      const fixture = harness({
        base: [
          ['foo', '1.4.2'],
          ['foo', '2.1.0'],
        ],
        head: [
          ['foo', '1.4.2'],
          ['foo', '0.9.0'],
        ],
        releases: {
          foo: [
            ['0.9.0', old],
          ],
        },
      })
      expect((await fixture.repair()).repairs).toEqual([])
    })
    it('fails when a repair itself would downgrade a locked version', async () => {
      const fixture = harness({
        base: [
          ['foo', '1.4.0'],
          ['bar', '1.2.0'],
        ],
        head: [
          ['foo', '1.4.2'],
          ['bar', '1.2.0'],
        ],
        releases: {
          foo: [
            ['1.4.1', old],
            ['1.4.2', young],
          ],
        },
        update: () => ({
          status: 0,
          packages: [
            ['foo', '1.4.1'],
            ['bar', '1.1.0'],
          ],
        }),
      })
      expect((await fixture.repair().catch(caught => caught)).message).toContain('cargo would downgrade bar from 1.2.0 to 1.1.0')
    })
  })
})

describe('updateWithRepair', () => {
  // A workspace whose `cargo update` writes `head`, and whose base commit locks `base`.
  function workspace({ before, head, releases, status = 0 }) {
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    const directory = mkdtempSync(resolve(root, '.tmp/cargo-update-'))
    fixtures.push(directory)
    const lockfile = join(directory, 'Cargo.lock')
    if (before !== null)
      writeFileSync(lockfile, cargoLock(before))
    const calls = []
    const options = {
      run: (args) => {
        calls.push(args[0])
        return args[0] === 'locate-project'
          ? { status: 0, stdout: `${join(directory, 'Cargo.toml')}\n`, stderr: '' }
          : { status: 101, stdout: '', stderr: 'unexpected cargo command' }
      },
      update: async () => {
        calls.push('update')
        if (status === 0)
          writeFileSync(lockfile, cargoLock(head))
        return { code: status, signal: null }
      },
      git: args => args[0] === 'cat-file'
        ? {
            status: before === null
              ? 1
              : 0,
            stdout: '',
            stderr: '',
          }
        : args[0] === 'rev-parse'
          ? { status: 0, stdout: 'abc\n', stderr: '' }
          : {
              status: 0,
              stdout: cargoLock(before),
              stderr: '',
            },
      registry: async name => (releases[name] ?? []).map(([num, created_at]) => ({ num, created_at, yanked: false })),
      policy: {
        cutoff,
        maxRepairRounds: 10,
        authorized: { crates: {} },
      },
    }
    return { lockfile, calls, options }
  }
  it('keeps the lockfile of an update that needs no repair', async () => {
    const { lockfile, options } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      head: [
        ['foo', '1.4.1'],
      ],
      releases: {
        foo: [
          ['1.4.1', old],
        ],
      },
    })
    expect(await updateWithRepair(options)).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      repairs: [],
      activeRoots: [],
    })
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.1']))
  })
  it('restores the lockfile of a failed repair to its state before the update', async () => {
    const { lockfile, options } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      head: [
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
      },
    })
    const error = await updateWithRepair(options).catch(caught => caught)
    expect(error).toBeInstanceOf(CargoRepairError)
    expect(error.message).toEndWith('- foo@1.4.2 (published 2026-10-30T00:00:00Z)\nCargo.lock was restored to its state before this command.')
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.0']))
  })
  it('restores the lockfile when a forced downgrade fails the repair', async () => {
    const { lockfile, options } = workspace({
      before: [
        ['foo', '1.4.2'],
      ],
      head: [
        ['foo', '1.4.1'],
      ],
      releases: {
        foo: [
          ['1.4.1', old],
        ],
      },
    })
    expect((await updateWithRepair(options).catch(caught => caught)).message).toContain('cargo would downgrade foo from 1.4.2 to 1.4.1')
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.2']))
  })
  it('removes a lockfile that did not exist before a failed update', async () => {
    const { lockfile, options } = workspace({
      before: null,
      head: [
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
      },
    })
    expect((await updateWithRepair(options).catch(caught => caught)).message).toEndWith('The new Cargo.lock was removed.')
    expect(existsSync(lockfile)).toBe(false)
  })
  it('selects no crates for a full update without an earlier lockfile, because no crate has a version to keep', async () => {
    const { lockfile, calls, options } = workspace({
      before: null,
      head: [
        ['foo', '1.4.2'],
      ],
      releases: {
        foo: [
          ['1.4.2', young],
        ],
      },
    })
    const error = await updateWithRepair({ ...options, selective: true }).catch(caught => caught)
    expect(error.reason).toBe('no eligible version')
    expect(error.message).toEndWith('The new Cargo.lock was removed.')
    // The harness answers any other cargo command with a failure, so none ran.
    expect(calls).toEqual(['locate-project', 'update'])
    expect(existsSync(lockfile)).toBe(false)
  })
  // A workspace whose full `cargo update` writes `updated`. Every `--precise` command fails,
  // as it does for a crate family that pins itself exactly. An update of specs writes the
  // packages that `select(specs)` returns, or fails with the status that it returns.
  function selectiveWorkspace({ before, updated, releases, select, selective = true, maxRepairRounds = 10 }) {
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    const directory = mkdtempSync(resolve(root, '.tmp/cargo-selective-'))
    fixtures.push(directory)
    const lockfile = join(directory, 'Cargo.lock')
    writeFileSync(lockfile, cargoLock(before))
    const selections = []
    const options = {
      run: (args) => {
        if (args[0] === 'locate-project')
          return { status: 0, stdout: `${join(directory, 'Cargo.toml')}\n`, stderr: '' }
        if (args[0] === 'metadata') {
          return {
            status: 0,
            stdout: JSON.stringify({ packages: [] }),
            stderr: '',
          }
        }
        if (args.includes('--precise'))
          return { status: 101, stdout: '', stderr: 'error: failed to select a version for the requirement' }
        selections.push(args.slice(1))
        const selected = select(args.slice(1))
        if (!Array.isArray(selected))
          return { status: selected.status, stdout: '', stderr: selected.stderr }
        writeFileSync(lockfile, cargoLock(selected))
        return { status: 0, stdout: '', stderr: '' }
      },
      update: async () => {
        writeFileSync(lockfile, cargoLock(updated))
        return { code: 0, signal: null }
      },
      git: args => args[0] === 'cat-file' || args[0] === 'rev-parse'
        ? { status: 0, stdout: 'abc\n', stderr: '' }
        : {
            status: 0,
            stdout: cargoLock(before),
            stderr: '',
          },
      registry: async name => (releases[name] ?? []).map(([num, created_at]) => ({ num, created_at, yanked: false })),
      policy: {
        cutoff,
        maxRepairRounds,
        authorized: { crates: {} },
      },
      selective,
    }
    return { lockfile, selections, options }
  }
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
        ['0.4.79', old],
        ['0.4.80', young],
      ],
      js: [
        ['0.3.106', old],
        ['0.3.107', young],
      ],
      sys: [
        ['0.3.106', old],
        ['0.3.107', young],
      ],
      other: [
        ['1.0.0', old],
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
  it('keeps a young crate family that pins itself exactly at its versions and updates every other crate of a full update', async () => {
    const { lockfile, selections, options } = selectiveWorkspace({
      ...family,
      select: () => familyKept,
    })
    expect(await updateWithRepair(options)).toEqual({
      result: { code: 0, signal: null },
      lockfile,
      repairs: [],
      activeRoots: [],
      excluded: ['futures', 'js', 'sys'],
    })
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
    expect(readFileSync(lockfile, 'utf8')).toBe(cargoLock(familyKept))
  })
  it('fails and restores the lockfile when cargo rejects the selective update', async () => {
    const { lockfile, selections, options } = selectiveWorkspace({
      ...family,
      select: () => ({ status: 101, stderr: 'error: failed to select a version for `other`' }),
    })
    const error = await updateWithRepair(options).catch(caught => caught)
    expect(error.reason).toBe('cargo rejected the selective update')
    expect(error.message).toContain('error: failed to select a version for `other`')
    expect(error.message).toEndWith('Cargo.lock was restored to its state before this command.')
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
    expect(readFileSync(lockfile, 'utf8')).toBe(cargoLock(family.before))
  })
  it('fails a targeted update whose repair fails, and never selects crates for it', async () => {
    const { lockfile, selections, options } = selectiveWorkspace({
      ...family,
      select: () => familyKept,
      selective: false,
    })
    expect((await updateWithRepair(options).catch(caught => caught)).reason).toBe('cargo rejected --precise 0.4.79')
    expect(selections).toEqual([])
    expect(readFileSync(lockfile, 'utf8')).toBe(cargoLock(family.before))
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
    const { lockfile, selections, options } = selectiveWorkspace({
      before: [
        ['foo', '1.4.1'],
        ['other', '1.0.0'],
        ['parent', '1.0.0'],
      ],
      updated: pulled,
      releases: {
        foo: [
          ['1.4.1', old],
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
    })
    expect(await updateWithRepair(options)).toMatchObject({
      excluded: ['foo', 'parent'],
    })
    expect(selections).toEqual([
      ['other@1.0.0', 'parent@1.0.0'],
      ['other@1.0.0'],
    ])
    expect(readFileSync(lockfile, 'utf8')).toBe(cargoLock(kept))
  })
  describe('a selective update whose selected crate still pulls a young version', () => {
    // The first round leaves out only foo. The selected parent pulls the young foo back,
    // so the second round leaves out parent too.
    const scenario = maxRepairRounds => selectiveWorkspace({
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
          ['1.4.1', old],
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
      maxRepairRounds,
    })
    it('repeats the selection without the crates that depend on the young version', async () => {
      const { selections, options } = scenario(10)
      expect(await updateWithRepair(options)).toMatchObject({
        excluded: ['foo', 'parent'],
      })
      expect(selections).toEqual([
        ['other@1.0.0', 'parent@1.0.0'],
        ['other@1.0.0'],
      ])
    })
    it('fails at the round limit and restores the lockfile', async () => {
      const { lockfile, selections, options } = scenario(1)
      const error = await updateWithRepair(options).catch(caught => caught)
      expect(error.reason).toBe('round limit reached')
      expect(error.message).toEndWith('Cargo.lock was restored to its state before this command.')
      expect(selections).toEqual([
        ['other@1.0.0', 'parent@1.0.0'],
      ])
      expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.1'], ['other', '1.0.0'], ['parent', '1.0.0']))
    })
  })
  it('leaves out the selected crate that adds a young crate with no old-enough version', async () => {
    const kept = [
      ['other', '1.1.0'],
      ['parent', '1.0.0'],
    ]
    const { selections, options } = selectiveWorkspace({
      before: [
        ['other', '1.0.0'],
        ['parent', '1.0.0'],
      ],
      updated: [
        ['newdep', '1.0.0'],
        ['other', '1.1.0'],
        [
          'parent',
          '1.1.0',
          ['newdep'],
        ],
      ],
      releases: {
        newdep: [
          ['1.0.0', young],
        ],
        other: [
          ['1.1.0', old],
        ],
        parent: [
          ['1.1.0', old],
        ],
      },
      select: () => kept,
    })
    expect(await updateWithRepair(options)).toMatchObject({
      excluded: ['newdep', 'parent'],
    })
    expect(selections).toEqual([
      ['other@1.0.0'],
    ])
  })
  it('fails with the repair error when no exclusion removes a crate from the selection', async () => {
    // A young new crate that only the workspace requires has no registry dependent to exclude.
    const { lockfile, selections, options } = selectiveWorkspace({
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
    })
    expect((await updateWithRepair(options).catch(caught => caught)).reason).toBe('no eligible version')
    expect(selections).toEqual([])
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['other', '1.0.0']))
  })
  it('never selects crates after a forced downgrade', async () => {
    const { lockfile, selections, options } = selectiveWorkspace({
      before: [
        ['foo', '1.4.2'],
      ],
      updated: [
        ['foo', '1.4.1'],
      ],
      releases: {
        foo: [
          ['1.4.1', old],
        ],
      },
      select: () => {
        throw new Error('Unexpected selective update.')
      },
    })
    expect((await updateWithRepair(options).catch(caught => caught)).message).toContain('cargo would downgrade foo from 1.4.2 to 1.4.1')
    expect(selections).toEqual([])
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.2']))
  })
  it('runs no repair after a failed update', async () => {
    const { lockfile, calls, options } = workspace({
      before: [
        ['foo', '1.4.0'],
      ],
      head: [],
      releases: {},
      status: 101,
    })
    expect(await updateWithRepair(options)).toEqual({
      result: { code: 101, signal: null },
      repairs: [],
      activeRoots: [],
    })
    expect(calls).toEqual(['locate-project', 'update'])
    expect(readFileSync(lockfile, 'utf8')).toBe(lockOf(['foo', '1.4.0']))
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

describe('cratesRegistry', () => {
  it('caches each crate and spaces requests one second apart across processes', async () => {
    mkdirSync(resolve(root, '.tmp'), { recursive: true })
    const directory = mkdtempSync(resolve(root, '.tmp/crates-registry-'))
    fixtures.push(directory)
    let clock = 10000
    const sleeps = []
    const requests = []
    const options = {
      api: 'https://crates.example/api/v1',
      cacheDirectory: join(directory, 'crates'),
      now: () => clock,
      sleep: async (ms) => {
        sleeps.push(ms)
        clock += ms
      },
      request: async (url) => {
        requests.push(url)
        return url.endsWith('/missing')
          ? new Response('', { status: 404 })
          : Response.json({
              versions: [
                {
                  num: '1.0.0',
                  created_at: old,
                  yanked: false,
                  extra: 'dropped',
                },
              ],
            })
      },
    }
    const registry = cratesRegistry(options)
    expect(await registry('foo')).toEqual([
      { num: '1.0.0', created_at: old, yanked: false },
    ])
    expect(await registry('foo')).toEqual([
      { num: '1.0.0', created_at: old, yanked: false },
    ])
    // A second process reads the same cache and the same timestamp file.
    expect(await cratesRegistry(options)('missing')).toEqual([])
    expect(requests).toEqual(['https://crates.example/api/v1/crates/foo', 'https://crates.example/api/v1/crates/missing'])
    expect(sleeps).toEqual([1000])
    await expect(cratesRegistry({
      ...options,
      request: async () => Response.json({}),
    })('broken')).rejects.toThrow('Invalid crate release metadata')
  })
})
