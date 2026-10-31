import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it } from 'bun:test'
import { bunResolves, main, shimPlan, trackSignals } from './renovate-tool-shim.mjs'
import { gitRepository, scratchDirectories } from './test-support.mjs'

const root = resolve(import.meta.dirname, '..')
const scratch = scratchDirectories()
afterEach(scratch.cleanup)

const policy = {
  ageSeconds: 259200,
  authorized: {
    'frontend/bun.lock': {
      'x@1.1.0': ['x-core@1.1.0'],
      'z@2.0.0': [],
    },
    '.github/renovate/bun.lock': {},
    'desktop/rust/Cargo.lock': {
      'fix@2.0.1': ['pin@1.0.5'],
    },
  },
}
const baseLock = '{ "packages": { "x": ["x@1.0.0", "", {}, "sha512-a"], "x-core": ["x-core@1.0.0", "", {}, "sha512-b"], "@s/y": ["@s/y@1.0.0", "", {}, "sha512-c"] } }'
const age = '--minimum-release-age=259200'
const excludes = plan => plan.args.find(arg => arg.startsWith('--minimum-release-age-excludes='))?.split('=')[1].split(',') ?? null
const project = value => () => ({ lockfile: 'frontend/bun.lock', ...value })

describe('shimPlan for bun', () => {
  it('adds the age and every base identity right after a resolving install', async () => {
    const plan = await shimPlan('bun', ['install', '--ignore-scripts'], policy, project({
      packageJson: {
        dependencies: { x: '^1.0.0' },
      },
      baseLock,
    }))
    expect(plan.args[0]).toBe('install')
    expect(plan.args[1]).toBe(age)
    expect(plan.args.at(-1)).toBe('--ignore-scripts')
    expect(excludes(plan)).toEqual(['@s/y@1.0.0', 'x-core@1.0.0', 'x@1.0.0'])
    expect(plan).toMatchObject({ agedUpdate: false, activeRoots: [], baseExcludes: 3 })
  })
  it('adds a declared authorized root and its closure', async () => {
    const plan = await shimPlan('bun', ['install', '--ignore-scripts'], policy, project({
      packageJson: {
        dependencies: { x: '1.1.0' },
      },
      baseLock,
    }))
    expect(excludes(plan)).toEqual([
      '@s/y@1.0.0',
      'x-core@1.0.0',
      'x-core@1.1.0',
      'x@1.0.0',
      'x@1.1.0',
    ])
    expect(plan.activeRoots).toEqual(['x@1.1.0'])
  })
  it('adds nothing for an undeclared root, a range, or a root that the base already locks', async () => {
    for (const packageJson of [
      {
        dependencies: { x: '^1.1.0' },
      },
      { dependencies: {} },
      null,
    ]) {
      const plan = await shimPlan('bun', ['install'], policy, project({ packageJson, baseLock }))
      expect(plan.activeRoots, JSON.stringify(packageJson)).toEqual([])
      expect(excludes(plan)).not.toContain('x@1.1.0')
    }
    const locked = '{ "packages": { "x": ["x@1.1.0", "", {}, "sha512-a"] } }'
    const plan = await shimPlan('bun', ['install'], policy, project({
      packageJson: {
        dependencies: { x: '1.1.0' },
      },
      baseLock: locked,
    }))
    expect(plan.activeRoots).toEqual([])
    expect(excludes(plan)).toEqual(['x@1.1.0'])
  })
  it('reads exact declarations from every dependency group', async () => {
    const plan = await shimPlan('bun', ['install'], policy, project({
      packageJson: {
        devDependencies: { z: '=2.0.0' },
        optionalDependencies: { x: 'v1.1.0' },
      },
      baseLock: null,
    }))
    expect(plan.activeRoots).toEqual(['x@1.1.0', 'z@2.0.0'])
  })
  it('takes the authorized roots of the project\'s own lockfile only', async () => {
    for (const lockfile of ['.github/renovate/bun.lock', 'other/bun.lock', null]) {
      const plan = await shimPlan('bun', ['install'], policy, project({
        packageJson: {
          dependencies: { x: '1.1.0' },
        },
        baseLock: null,
        lockfile,
      }))
      expect(plan.activeRoots, String(lockfile)).toEqual([])
      expect(excludes(plan), String(lockfile)).toBeNull()
    }
  })
  it('passes every call that does not resolve versions through unchanged, without reading the project', async () => {
    const readProject = () => {
      throw new Error('A call that passes through must not read its project.')
    }
    for (const argv of [
      ['install', '--frozen-lockfile'],
      ['i', '--frozen-lockfile'],
      ['install', '--frozen-lockfile=true'],
      ['run', 'x'],
      ['x', 'cowsay'],
      ['x.mjs'],
      ['scripts/renovate-post-upgrade.mjs'],
      ['--version'],
      ['--cwd', 'frontend', 'install'],
      [],
    ])
      expect(await shimPlan('bun', argv, policy, readProject), JSON.stringify(argv)).toEqual({ args: argv, agedUpdate: false })
  })
  it('adds the age to every resolving subcommand, and omits an empty exclude list', async () => {
    for (const command of [
      'add',
      'a',
      'update',
      'remove',
      'rm',
      'link',
      'patch',
    ]) {
      const plan = await shimPlan('bun', [command, 'pkg'], policy, project({ packageJson: {}, baseLock: null }))
      expect(plan.args, command).toEqual([command, age, 'pkg'])
      expect(plan.baseExcludes).toBe(0)
    }
  })
  it('replaces a release age that the caller passes, and never moves an argument after --', async () => {
    const plan = await shimPlan('bun', [
      'install',
      '--minimum-release-age=1',
      '--minimum-release-age-excludes',
      'x',
      '--ignore-scripts',
    ], policy, project({ packageJson: {}, baseLock: null }))
    expect(plan.args).toEqual(['install', age, '--ignore-scripts'])
    const terminated = await shimPlan('bun', [
      'add',
      'foo',
      '--',
      '--minimum-release-age=1',
    ], policy, project({ packageJson: {}, baseLock: null }))
    expect(terminated.args).toEqual([
      'add',
      age,
      'foo',
      '--',
      '--minimum-release-age=1',
    ])
  })
})

describe('bunResolves', () => {
  it('takes the first argument that is not an option as the command, as Bun does', () => {
    // Bun runs `bun --cwd frontend install` as the script `frontend`.
    expect(bunResolves(['--cwd', 'frontend', 'install'])).toBe(false)
    expect(bunResolves(['--cwd=frontend', 'install'])).toBe(true)
    expect(bunResolves(['--cwd=frontend', 'install', '--frozen-lockfile'])).toBe(false)
    expect(bunResolves(['-c', 'bunfig.toml', 'run', 'install'])).toBe(false)
  })
  it('treats an install with a false frozen-lockfile option as resolving', () => {
    expect(bunResolves(['install', '--frozen-lockfile=false'])).toBe(true)
    expect(bunResolves(['install', '--frozen-lockfile=true'])).toBe(false)
  })
})

describe('shimPlan for cargo', () => {
  it('runs each update through the aged index', async () => {
    for (const argv of [
      ['update'],
      ['update', '--workspace'],
      ['+1.98.0', 'update'],
    ])
      expect((await shimPlan('cargo', argv, policy)).agedUpdate, JSON.stringify(argv)).toBe(true)
    expect((await shimPlan('cargo', ['+1.98.0', 'update'])).toolchain).toEqual(['+1.98.0'])
  })
  it('keeps the manifest and configuration options and the precise target for the commands of the check', async () => {
    const plan = await shimPlan('cargo', [
      'update',
      '--config',
      'net.git-fetch-with-cli=true',
      '--manifest-path',
      'desktop/rust/Cargo.toml',
      '--package',
      'fix@2.0.0',
      '--precise',
      '2.0.1',
    ], policy)
    expect(plan).toMatchObject({
      agedUpdate: true,
      toolchain: [],
      manifestArgs: [
        '--config',
        'net.git-fetch-with-cli=true',
        '--manifest-path',
        'desktop/rust/Cargo.toml',
      ],
      preciseTarget: 'fix@2.0.1',
    })
    expect(await shimPlan('cargo', [
      'update',
      '-p',
      'registry+https://github.com/rust-lang/crates.io-index#fix@2.0.0',
      '--precise=2.0.1',
      '--manifest-path=Cargo.toml',
    ], policy)).toMatchObject({
      manifestArgs: ['--manifest-path=Cargo.toml'],
      preciseTarget: 'fix@2.0.1',
    })
    expect((await shimPlan('cargo', ['update', '--workspace'], policy)).preciseTarget).toBeNull()
  })
  it('reads an attached package spec and the short manifest option', async () => {
    expect(await shimPlan('cargo', [
      'update',
      '-pfix@2.0.0',
      '--precise',
      '2.0.1',
      '-m',
      'Cargo.toml',
    ], policy)).toMatchObject({
      manifestArgs: ['--manifest-path', 'Cargo.toml'],
      preciseTarget: 'fix@2.0.1',
      fullUpdate: false,
    })
  })
  it('rejects the options that move the lockfile or the directory of the check', async () => {
    for (const argv of [
      ['-C', 'other', 'update'],
      ['-Cother', 'update'],
      [
        'update',
        '-Z',
        'unstable-options',
        '--lockfile-path',
        '/ci/Cargo.lock',
      ],
      ['update', '--lockfile-path=/ci/Cargo.lock'],
    ])
      await expect(shimPlan('cargo', argv, policy), JSON.stringify(argv)).rejects.toThrow('cannot hold cargo update to the age')
  })
  it('selects crates after a failed check only for an update of every crate', async () => {
    for (const argv of [
      ['update'],
      ['+1.98.0', 'update'],
      [
        'update',
        '--config',
        'net.git-fetch-with-cli=true',
        '--manifest-path',
        'desktop/rust/Cargo.toml',
      ],
      ['update', '--manifest-path=desktop/rust/Cargo.toml', '--dry-run'],
      ['update', '-m', 'desktop/rust/Cargo.toml'],
      [
        '--config',
        'net.retry=2',
        'update',
        '-Z',
        'unstable-options',
      ],
    ])
      expect((await shimPlan('cargo', argv, policy)).fullUpdate, JSON.stringify(argv)).toBe(true)
    for (const argv of [
      ['update', '--workspace'],
      ['update', '-w'],
      ['update', '-p', 'foo@1.0.0'],
      ['update', '-pfoo'],
      ['update', '--package=foo'],
      [
        'update',
        '--package',
        'foo',
        '--precise',
        '1.0.1',
      ],
      ['update', '--precise=1.0.1', 'foo'],
      [
        'update',
        '--manifest-path',
        'Cargo.toml',
        'foo',
      ],
    ])
      expect((await shimPlan('cargo', argv, policy)).fullUpdate, JSON.stringify(argv)).toBe(false)
  })
  it('skips the value of a global option before the subcommand', async () => {
    expect((await shimPlan('cargo', ['--config', 'update=x', 'metadata'], policy)).agedUpdate).toBe(false)
    expect((await shimPlan('cargo', ['--config', 'net.retry=2', 'update'], policy)).agedUpdate).toBe(true)
  })
  it('passes every other subcommand and a request for help through unchanged', async () => {
    for (const argv of [
      ['metadata', '--format-version', '1'],
      ['locate-project'],
      ['generate-lockfile'],
      ['update', '--help'],
      ['update', '-h'],
      ['--version'],
      [],
    ])
      expect(await shimPlan('cargo', argv, policy), JSON.stringify(argv)).toEqual({ args: argv, agedUpdate: false })
  })
  it('rejects an unknown tool', async () => {
    await expect(shimPlan('npm', [], policy)).rejects.toThrow('Unknown shimmed tool')
  })
})

describe('trackSignals', () => {
  it('records the first signal, forwards each one to the attached child, and stops listening when released', () => {
    const listeners = process.listenerCount('SIGUSR2')
    const tracked = trackSignals(['SIGUSR2'])
    try {
      expect(() => tracked.assertRunning()).not.toThrow()
      const killed = []
      tracked.attach({
        kill: signal => killed.push(signal),
      })
      process.emit('SIGUSR2', 'SIGUSR2')
      tracked.detach()
      process.emit('SIGUSR2', 'SIGUSR2')
      expect(killed).toEqual(['SIGUSR2'])
      expect(tracked.pending).toBe('SIGUSR2')
      expect(() => tracked.assertRunning()).toThrow('The shim received SIGUSR2.')
    }
    finally {
      tracked.release()
    }
    expect(process.listenerCount('SIGUSR2')).toBe(listeners)
  })
  // The child that sends the signal is a POSIX shell script.
  it.skipIf(process.platform === 'win32')('delivers a signal that arrives during a synchronous call once the event loop turns', async () => {
    const tracked = trackSignals(['SIGUSR2'])
    try {
      spawnSync('sh', ['-c', 'kill -USR2 $PPID; sleep 0.1'])
      // The handler cannot run while spawnSync blocks the event loop, nor right after it.
      expect(tracked.pending).toBeNull()
      await tracked.deliverPending()
      expect(tracked.pending).toBe('SIGUSR2')
    }
    finally {
      // A signal that reaches no listener would end the test process.
      await new Promise(resolve => setTimeout(resolve, 50))
      tracked.release()
    }
  })
})

// The fake tools are POSIX shell scripts. Renovate runs only on Linux.
describe.skipIf(process.platform === 'win32')('main', () => {
  // A policy whose real Bun and real cargo are scripts that record their arguments in
  // `ran`, then run `body`. A cargo body runs only when the test supplies one, in a
  // repository, as the age policy of a cargo update reads the base of its lockfile.
  function shimFixture(body, { cargo } = {}) {
    const directory = scratch.create('renovate-tool-shim')
    if (cargo !== undefined)
      gitRepository(directory)
    const bun = join(directory, 'bun')
    const ran = join(directory, 'ran')
    writeFileSync(bun, `#!/bin/sh\nprintf '%s\\n' "$@" > '${ran}'\n${body}\n`)
    chmodSync(bun, 0o755)
    if (cargo !== undefined) {
      writeFileSync(join(directory, 'cargo'), `#!/bin/sh\n${cargo(directory)}\nprintf '%s\\n' "$@" > '${ran}'\n`)
      chmodSync(join(directory, 'cargo'), 0o755)
    }
    const policyPath = join(directory, 'policy.json')
    const invocationLog = join(directory, 'invocations.jsonl')
    writeFileSync(policyPath, JSON.stringify({
      ...policy,
      cutoff: '2026-10-28T00:00:00Z',
      realTools: {
        bun,
        cargo: join(directory, 'cargo'),
      },
      invocationLog,
    }))
    return {
      policyPath,
      ran: () => existsSync(ran)
        ? readFileSync(ran, 'utf8').trim().split('\n')
        : null,
      records: () => existsSync(invocationLog)
        ? readFileSync(invocationLog, 'utf8').trim().split('\n').map(line => JSON.parse(line))
        : [],
    }
  }
  const signals = () => trackSignals(['SIGUSR2'])
  it('runs the real tool, returns its exit code, records the call, and releases the signal handlers', async () => {
    const shim = shimFixture('exit 3')
    const listeners = process.listenerCount('SIGUSR2')
    const result = await main(['bun', shim.policyPath, 'run', 'x'], {
      signals: signals(),
    })
    expect(result).toMatchObject({ exitCode: 3, signal: null })
    expect(shim.ran()).toEqual(['run', 'x'])
    expect(shim.records()).toEqual([
      expect.objectContaining({
        tool: 'bun',
        args: ['run', 'x'],
        effectiveArgs: ['run', 'x'],
        activeRoots: [],
        baseExcludes: 0,
        exitCode: 3,
      }),
    ])
    expect(shim.records()[0]).not.toHaveProperty('signal')
    expect(process.listenerCount('SIGUSR2')).toBe(listeners)
  })
  it('returns the signal that ended the real tool', async () => {
    const shim = shimFixture('kill -TERM $$')
    const result = await main(['bun', shim.policyPath, '--version'], {
      signals: signals(),
    })
    expect(result).toMatchObject({ exitCode: null, signal: 'SIGTERM' })
    expect(shim.records()).toEqual([
      expect.objectContaining({ exitCode: null, signal: 'SIGTERM' }),
    ])
  })
  it('never starts the real tool after a signal arrives during the planning', async () => {
    const shim = shimFixture('exit 0')
    const tracked = signals()
    process.emit('SIGUSR2', 'SIGUSR2')
    const result = await main(['bun', shim.policyPath, '--version'], { signals: tracked })
    expect(result).toMatchObject({ exitCode: 1, signal: 'SIGUSR2' })
    expect(shim.ran()).toBeNull()
    expect(shim.records()).toEqual([
      expect.objectContaining({ error: 'The shim received SIGUSR2.', exitCode: 1, signal: 'SIGUSR2' }),
    ])
  })
  it('never starts cargo update after a signal arrives during a synchronous cargo call', async () => {
    // cargo locate-project runs through spawnSync and sends the signal to the shim.
    const shim = shimFixture('exit 0', {
      cargo: directory => `if [ "$1" = locate-project ]; then kill -USR2 $PPID; printf '%s\\n' '${join(directory, 'Cargo.toml')}'; exit 0; fi`,
    })
    const result = await main(['cargo', shim.policyPath, 'update'], {
      signals: signals(),
    })
    expect(result).toMatchObject({ exitCode: 1, signal: 'SIGUSR2' })
    expect(shim.ran()).toBeNull()
    expect(shim.records()).toEqual([
      expect.objectContaining({ error: 'The shim received SIGUSR2.', exitCode: 1, signal: 'SIGUSR2' }),
    ])
  })
  it('runs cargo update through the aged index, with its options after the toolchain', async () => {
    // The fake cargo answers locate-project, and its update writes an empty lockfile.
    const shim = shimFixture('exit 0', {
      cargo: directory => `case " $* " in *" locate-project "*) printf '%s\\n' '${join(directory, 'Cargo.toml')}'; exit 0;; *" update "*) printf 'version = 4\\n' > '${join(directory, 'Cargo.lock')}';; esac`,
    })
    const result = await main([
      'cargo',
      shim.policyPath,
      '+1.98.0',
      'update',
      '--workspace',
    ], {
      signals: signals(),
    })
    expect(result).toMatchObject({ exitCode: 0, signal: null })
    const args = shim.ran()
    expect(args[0]).toBe('+1.98.0')
    expect(args.slice(1, 4)).toEqual(['--config', 'source.crates-io.replace-with="leapmux-aged-index"', '--config'])
    expect(args[4]).toMatch(/^source\.leapmux-aged-index\.registry="sparse\+http:\/\/127\.0\.0\.1:\d+\/"$/)
    expect(args.slice(5)).toEqual(['update', '--workspace'])
    expect(shim.records()).toEqual([
      expect.objectContaining({
        tool: 'cargo',
        args: ['+1.98.0', 'update', '--workspace'],
        activeRoots: [],
        exitCode: 0,
      }),
    ])
  })
  it('lets a signal from the last synchronous call reach its handler before it records the outcome', async () => {
    const shim = shimFixture('exit 0')
    // Model a signal that arrives after the real tool starts, during a synchronous call whose
    // handler runs at the next turn of the event loop: only the second delivery finds it.
    let deliveries = 0
    let pending = null
    const result = await main(['bun', shim.policyPath, '--version'], {
      signals: {
        get pending() {
          return pending
        },
        attach() {},
        detach() {},
        assertRunning() {},
        release() {},
        async deliverPending() {
          if (++deliveries === 2)
            pending = 'SIGUSR2'
        },
      },
    })
    expect(shim.ran()).toEqual(['--version'])
    expect(result).toMatchObject({ exitCode: 0, signal: 'SIGUSR2' })
    expect(shim.records()).toEqual([
      expect.objectContaining({ exitCode: 0, signal: 'SIGUSR2' }),
    ])
  })
  it('records a call that the shim cannot plan, without starting the real tool', async () => {
    const shim = shimFixture('exit 0')
    const result = await main([
      'cargo',
      shim.policyPath,
      '-C',
      'other',
      'update',
    ], {
      signals: signals(),
    })
    expect(result).toMatchObject({ exitCode: 1, signal: null })
    expect(shim.records()).toEqual([
      expect.objectContaining({
        error: expect.stringContaining('cannot hold cargo update to the age'),
        exitCode: 1,
      }),
    ])
  })
  it('releases the signal handlers when it cannot read the policy', async () => {
    const listeners = process.listenerCount('SIGUSR2')
    await expect(main([
      'bun',
      resolve(root, '.tmp/absent-policy.json'),
      '--version',
    ], {
      signals: signals(),
    })).rejects.toThrow('ENOENT')
    expect(process.listenerCount('SIGUSR2')).toBe(listeners)
  })
})
