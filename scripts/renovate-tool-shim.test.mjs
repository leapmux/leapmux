import { describe, expect, it } from 'bun:test'
import { bunResolves, shimPlan } from './renovate-tool-shim.mjs'

const policy = {
  ageSeconds: 259200,
  authorized: {
    npm: {
      'x@1.1.0': ['x-core@1.1.0'],
      'z@2.0.0': [],
    },
    crates: {
      'fix@2.0.1': ['pin@1.0.5'],
    },
  },
}
const baseLock = '{ "packages": { "x": ["x@1.0.0", "", {}, "sha512-a"], "x-core": ["x-core@1.0.0", "", {}, "sha512-b"], "@s/y": ["@s/y@1.0.0", "", {}, "sha512-c"] } }'
const age = '--minimum-release-age=259200'
const excludes = plan => plan.args.find(arg => arg.startsWith('--minimum-release-age-excludes='))?.split('=')[1].split(',') ?? null

describe('shimPlan for bun', () => {
  it('adds the age and every base identity to a resolving install', () => {
    const plan = shimPlan('bun', ['install', '--ignore-scripts'], policy, {
      packageJson: {
        dependencies: { x: '^1.0.0' },
      },
      baseLock,
    })
    expect(plan.args.slice(0, 3)).toEqual(['install', '--ignore-scripts', age])
    expect(excludes(plan)).toEqual(['@s/y@1.0.0', 'x-core@1.0.0', 'x@1.0.0'])
    expect(plan).toMatchObject({ repair: false, activeRoots: [], baseExcludes: 3 })
  })
  it('adds a declared authorized root and its closure', () => {
    const plan = shimPlan('bun', ['install', '--ignore-scripts'], policy, {
      packageJson: {
        dependencies: { x: '1.1.0' },
      },
      baseLock,
    })
    expect(excludes(plan)).toEqual([
      '@s/y@1.0.0',
      'x-core@1.0.0',
      'x-core@1.1.0',
      'x@1.0.0',
      'x@1.1.0',
    ])
    expect(plan.activeRoots).toEqual(['x@1.1.0'])
  })
  it('adds nothing for an undeclared root, a range, or a root that the base already locks', () => {
    for (const packageJson of [
      {
        dependencies: { x: '^1.1.0' },
      },
      { dependencies: {} },
      null,
    ]) {
      const plan = shimPlan('bun', ['install'], policy, { packageJson, baseLock })
      expect(plan.activeRoots, JSON.stringify(packageJson)).toEqual([])
      expect(excludes(plan)).not.toContain('x@1.1.0')
    }
    const locked = '{ "packages": { "x": ["x@1.1.0", "", {}, "sha512-a"] } }'
    const plan = shimPlan('bun', ['install'], policy, {
      packageJson: {
        dependencies: { x: '1.1.0' },
      },
      baseLock: locked,
    })
    expect(plan.activeRoots).toEqual([])
    expect(excludes(plan)).toEqual(['x@1.1.0'])
  })
  it('reads exact declarations from every dependency group', () => {
    const plan = shimPlan('bun', ['install'], policy, {
      packageJson: {
        devDependencies: { z: '=2.0.0' },
        optionalDependencies: { x: 'v1.1.0' },
      },
      baseLock: null,
    })
    expect(plan.activeRoots).toEqual(['x@1.1.0', 'z@2.0.0'])
  })
  it('passes every call that does not resolve versions through unchanged', () => {
    for (const argv of [
      ['install', '--frozen-lockfile'],
      ['i', '--frozen-lockfile'],
      ['run', 'x'],
      ['x.mjs'],
      ['scripts/renovate-post-upgrade.mjs'],
      ['--version'],
      [],
    ])
      expect(shimPlan('bun', argv, policy, {}), JSON.stringify(argv)).toEqual({ args: argv, repair: false })
  })
  it('adds the age to add and update, and omits an empty exclude list', () => {
    for (const command of ['add', 'update']) {
      const plan = shimPlan('bun', [command, 'pkg'], policy, { packageJson: {}, baseLock: null })
      expect(plan.args).toEqual([command, 'pkg', age])
      expect(plan.baseExcludes).toBe(0)
    }
  })
  it('replaces a release age that the caller passes', () => {
    const plan = shimPlan('bun', [
      'install',
      '--minimum-release-age=1',
      '--minimum-release-age-excludes',
      'x',
      '--ignore-scripts',
    ], policy, { packageJson: {}, baseLock: null })
    expect(plan.args).toEqual(['install', '--ignore-scripts', age])
  })
})

describe('bunResolves', () => {
  it('finds the subcommand after a global option with a value', () => {
    expect(bunResolves(['--cwd', 'frontend', 'install'])).toBe(true)
    expect(bunResolves(['--cwd=frontend', 'install', '--frozen-lockfile'])).toBe(false)
    expect(bunResolves(['-c', 'bunfig.toml', 'run', 'install'])).toBe(false)
  })
})

describe('shimPlan for cargo', () => {
  it('requests a repair after each update', () => {
    for (const argv of [
      ['update'],
      ['update', '--workspace'],
      ['+1.98.0', 'update'],
    ])
      expect(shimPlan('cargo', argv, policy).repair, JSON.stringify(argv)).toBe(true)
    expect(shimPlan('cargo', ['+1.98.0', 'update']).toolchain).toEqual(['+1.98.0'])
  })
  it('keeps the manifest and configuration options and the precise target for the repair commands', () => {
    const plan = shimPlan('cargo', [
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
      repair: true,
      toolchain: [],
      manifestArgs: [
        '--config',
        'net.git-fetch-with-cli=true',
        '--manifest-path',
        'desktop/rust/Cargo.toml',
      ],
      preciseTarget: 'fix@2.0.1',
    })
    expect(shimPlan('cargo', [
      'update',
      '-p',
      'registry+https://github.com/rust-lang/crates.io-index#fix@2.0.0',
      '--precise=2.0.1',
      '--manifest-path=Cargo.toml',
    ], policy)).toMatchObject({
      manifestArgs: ['--manifest-path=Cargo.toml'],
      preciseTarget: 'fix@2.0.1',
    })
    expect(shimPlan('cargo', ['update', '--workspace'], policy).preciseTarget).toBeNull()
  })
  it('selects crates after a failed repair only for an update of every crate', () => {
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
      [
        '--config',
        'net.retry=2',
        'update',
        '-Z',
        'unstable-options',
      ],
    ])
      expect(shimPlan('cargo', argv, policy).selective, JSON.stringify(argv)).toBe(true)
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
      expect(shimPlan('cargo', argv, policy).selective, JSON.stringify(argv)).toBe(false)
  })
  it('skips the value of a global option before the subcommand', () => {
    expect(shimPlan('cargo', ['--config', 'update=x', 'metadata'], policy).repair).toBe(false)
    expect(shimPlan('cargo', ['--config', 'net.retry=2', 'update'], policy).repair).toBe(true)
  })
  it('passes every other subcommand through unchanged', () => {
    for (const argv of [
      ['metadata', '--format-version', '1'],
      ['locate-project'],
      ['--version'],
      [],
    ])
      expect(shimPlan('cargo', argv, policy), JSON.stringify(argv)).toEqual({ args: argv, repair: false })
  })
  it('rejects an unknown tool', () => {
    expect(() => shimPlan('npm', [], policy)).toThrow('Unknown shimmed tool')
  })
})
