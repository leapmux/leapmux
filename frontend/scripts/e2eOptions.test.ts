import { createRequire } from 'node:module'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isObject } from '../src/lib/jsonPick'
import { discoveryRunArgs, parseE2EOptions, serialRunArgs, shardRunArgs, shardSelectionArgs } from './e2eOptions'

afterEach(() => vi.unstubAllEnvs())

describe('parseE2EOptions', () => {
  it('limits the default shard count to four and the available CPU capacity', () => {
    expect(parseE2EOptions([], 10).workers).toBe(4)
    expect(parseE2EOptions([], 2).workers).toBe(2)
    expect(parseE2EOptions([], 1).serial).toBe(true)
  })

  it('reports the launcher defaults', () => {
    expect(parseE2EOptions([], 10)).toEqual({
      workers: 4,
      playwrightArgs: [],
      lastFailed: false,
      failedFiles: false,
      passWithNoTests: false,
      testList: false,
      balance: 'history',
      serial: false,
    })
  })

  it.each([{ args: ['--workers=2'] }, { args: ['--workers', '2'] }, { args: ['-j', '2'] }, { args: ['-j2'] }])('consumes the public worker count without forwarding it: %j', ({ args }) => {
    expect(parseE2EOptions([...args, 'provider/spec with spaces.spec.ts'], 10))
      .toMatchObject({ workers: 2, playwrightArgs: ['provider/spec with spaces.spec.ts'], serial: false })
  })

  it('selects one serial process with --workers=1', () => {
    expect(parseE2EOptions(['--workers=1'], 10)).toMatchObject({ workers: 1, serial: true })
  })

  it.each([['50%', 5], ['1%', 1], ['100%', 10]])('accepts the native percentage syntax %s', (value, expected) => {
    expect(parseE2EOptions([`--workers=${value}`], 10).workers).toBe(expected)
  })

  it.each(['0', '-1', '1.5', 'NaN', 'Infinity', '01', '0%', '101%', '9007199254740992'])('rejects an invalid count: %j', (value) => {
    expect(() => parseE2EOptions([`--workers=${value}`], 10)).toThrow('worker count')
  })

  it.each([{ args: ['--workers'] }, { args: ['--workers='] }, { args: ['--workers', '--grep'] }, { args: ['--workers=2', '-j', '3'] }])('rejects absent or repeated counts: %j', ({ args }) => {
    expect(() => parseE2EOptions(args, 10)).toThrow('E2E')
  })

  it.each(['--headed', '--debug', '--ui', '--list', '--shard=1/3', '--config=other.ts', '--max-failures=1', '-x', '--global-timeout=1000'])('keeps interactive and externally selected runs serial: %s', (flag) => {
    expect(parseE2EOptions(['--workers=4', flag], 10)).toMatchObject({ workers: 1, serial: true, playwrightArgs: [flag] })
  })

  it('preserves exact reporter, output, grep, project, and file arguments', () => {
    const args = ['--reporter', 'json', '--output=folder with spaces', '--grep', 'pattern with spaces', '--project=mock-chromium', 'provider/file.spec.ts']
    expect(parseE2EOptions(args, 10)).toMatchObject({ playwrightArgs: args, reporters: 'json', outputDir: 'folder with spaces' })
  })

  it('keeps filters after the argument separator unchanged', () => {
    expect(parseE2EOptions(['--', '--workers=2', '--fully-parallel'], 10).playwrightArgs)
      .toEqual(['--', '--workers=2', '--fully-parallel'])
  })

  it.each(['--grep', '-g', '--grep-invert', '-G'])('preserves an option-like regular expression after %s', (option) => {
    const args = [option, '--workers=2', '--workers=3']
    expect(parseE2EOptions(args, 10)).toMatchObject({ workers: 3, playwrightArgs: [option, '--workers=2'] })
  })

  it.each(['1', 'inspector'])('keeps an environment debugger serial: %s', (mode) => {
    vi.stubEnv('PWDEBUG', mode)
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 1, serial: true })
  })

  it.each(['0', 'false', '', 'console'])('keeps a headless environment debugger mode parallel: %j', (mode) => {
    vi.stubEnv('PWDEBUG', mode)
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 3, serial: false })
  })

  it.each(['npm_config_pwdebug', 'npm_package_config_pwdebug'])('uses the native fallback inspector setting: %s', (key) => {
    vi.stubEnv('PWDEBUG', undefined)
    vi.stubEnv('npm_config_pwdebug', undefined)
    vi.stubEnv(key, '1')
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 1, serial: true })
    vi.stubEnv('PWDEBUG', '')
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 3, serial: false })
  })

  it('keeps a compact failure limit and worker count serial', () => {
    expect(parseE2EOptions(['-xj2'], 10)).toMatchObject({ workers: 1, serial: true, playwrightArgs: ['-x'] })
  })

  it('preserves an empty native regular expression', () => {
    expect(parseE2EOptions(['--grep', ''], 10).playwrightArgs).toEqual(['--grep', ''])
  })

  it('preserves an attached optional short value that resembles the worker option', () => {
    expect(parseE2EOptions(['-u--workers=2'], 10).playwrightArgs).toEqual(['--update-snapshots=--workers=2'])
  })

  it.each(['--update-snapshots=all', '-u', '--update-source-method=patch'])('keeps source and snapshot updates serial: %s', (flag) => {
    expect(parseE2EOptions([flag], 10)).toMatchObject({ workers: 1, serial: true })
  })

  it.each([{ args: ['--update-snapshots', 'all'] }, { args: ['-u', 'all'] }, { args: ['--debug', 'cli'] }, { args: ['--only-changed', 'main'] }])('keeps an optional native value with its option: %j', ({ args }) => {
    expect(parseE2EOptions(args, 10).playwrightArgs).toEqual(args)
  })

  it.each(['--fully-parallel', '--fully-parallel=true'])('rejects unsafe shared-fixture concurrency: %s', (flag) => {
    expect(() => parseE2EOptions([flag], 10)).toThrow('serial tests inside each isolated shard')
  })

  it.each(['--retries=1', '--retries=-1', '--retries=NaN'])('rejects an override of the zero-retry rule: %s', (flag) => {
    expect(() => parseE2EOptions([flag], 10)).toThrow('retries')
  })

  it('accepts an explicit zero retry count', () => {
    expect(parseE2EOptions(['--retries', '0'], 10).playwrightArgs).toEqual(['--retries', '0'])
  })

  it.each(['--parallelism=2', '--parellelism=1'])('identifies the supported public option: %s', (flag) => {
    expect(() => parseE2EOptions([flag], 10)).toThrow('Use --workers')
  })

  it.each([0, -1, 0.5, Number.NaN, Number.POSITIVE_INFINITY])('rejects an invalid CPU capacity: %j', (capacity) => {
    expect(() => parseE2EOptions([], capacity)).toThrow('CPU capacity')
  })
})

describe('parseE2EOptions last-failed selection', () => {
  it('keeps --last-failed parallel and forwards it for a serial run', () => {
    expect(parseE2EOptions(['--workers=3', '--last-failed', 'provider/file.spec.ts'], 10))
      .toMatchObject({ workers: 3, serial: false, lastFailed: true, playwrightArgs: ['--last-failed', 'provider/file.spec.ts'] })
  })

  it.each([{ args: ['--last-failed-file=state dir/last.json'] }, { args: ['--last-failed-file', 'state dir/last.json'] }])('keeps an explicit last-failed file parallel: %j', ({ args }) => {
    expect(parseE2EOptions(['--workers=3', '--last-failed', ...args], 10))
      .toMatchObject({ workers: 3, serial: false, lastFailed: true, lastFailedFile: 'state dir/last.json', playwrightArgs: ['--last-failed', ...args] })
  })

  it('keeps an explicit environment destination for the last run parallel', () => {
    vi.stubEnv('PLAYWRIGHT_LAST_RUN_OUTPUT_FILE', 'caller-last-run.json')
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 3, serial: false })
  })

  it('accepts a last-failed file without --last-failed, which changes only the state destination natively', () => {
    expect(parseE2EOptions(['--last-failed-file=state.json'], 10)).toMatchObject({ lastFailed: false, lastFailedFile: 'state.json' })
  })

  it.each([{ args: ['--last-failed-file='] }, { args: ['--last-failed-file', ''] }])('rejects an empty last-failed file: %j', ({ args }) => {
    expect(() => parseE2EOptions(args, 10)).toThrow('--last-failed-file option requires a nonempty path')
  })

  it('rejects a repeated last-failed file', () => {
    expect(() => parseE2EOptions(['--last-failed-file=one.json', '--last-failed-file=two.json'], 10)).toThrow('must appear only once')
  })

  it.each(['--last-failed=true', '--pass-with-no-tests=false', '--failed-files=yes'])('rejects a value on a launcher-inspected flag: %s', (flag) => {
    expect(() => parseE2EOptions([flag], 10)).toThrow('takes no value')
  })

  it('reads --pass-with-no-tests only as a flag, never as a regular expression value', () => {
    expect(parseE2EOptions(['--pass-with-no-tests'], 10)).toMatchObject({ passWithNoTests: true, playwrightArgs: ['--pass-with-no-tests'] })
    expect(parseE2EOptions(['--grep', '--pass-with-no-tests'], 10)).toMatchObject({ passWithNoTests: false, playwrightArgs: ['--grep', '--pass-with-no-tests'] })
    expect(parseE2EOptions(['--', '--pass-with-no-tests'], 10).passWithNoTests).toBe(false)
  })

  it('reads --last-failed only as a flag, never as a regular expression value', () => {
    expect(parseE2EOptions(['-g', '--last-failed'], 10).lastFailed).toBe(false)
  })

  it.each([{ args: ['--test-list=list.txt'] }, { args: ['--test-list', 'list.txt'] }])('reports a caller test list: %j', ({ args }) => {
    expect(parseE2EOptions(args, 10)).toMatchObject({ testList: true, playwrightArgs: args })
  })

  it('does not report an inverted test list as a test list', () => {
    expect(parseE2EOptions(['--test-list-invert=list.txt'], 10).testList).toBe(false)
  })
})

describe('parseE2EOptions balance', () => {
  it.each([{ args: ['--balance=off'], mode: 'off' }, { args: ['--balance', 'off'], mode: 'off' }, { args: ['--balance=history'], mode: 'history' }])('consumes the balance mode without forwarding it: %j', ({ args, mode }) => {
    expect(parseE2EOptions([...args, 'provider/file.spec.ts'], 10)).toMatchObject({ balance: mode, playwrightArgs: ['provider/file.spec.ts'] })
  })

  it.each(['--balance=', '--balance=on', '--balance=OFF', '--balance=duration'])('rejects an unknown balance mode: %s', (flag) => {
    expect(() => parseE2EOptions([flag], 10)).toThrow('--balance option must be "history" or "off"')
  })

  it('rejects an absent balance mode', () => {
    expect(() => parseE2EOptions(['--balance'], 10)).toThrow('--balance option requires a value')
  })

  it('rejects a repeated balance mode', () => {
    expect(() => parseE2EOptions(['--balance=off', '--balance=off'], 10)).toThrow('--balance option must appear only once')
  })

  it('accepts a balance mode in a serial run, where it changes nothing', () => {
    expect(parseE2EOptions(['--workers=1', '--balance=off'], 10)).toMatchObject({ serial: true, balance: 'off', playwrightArgs: [] })
  })
})

describe('parseE2EOptions failed files', () => {
  it('consumes --failed-files without forwarding it', () => {
    expect(parseE2EOptions(['--failed-files', '--timeout', '5000'], 10))
      .toMatchObject({ failedFiles: true, playwrightArgs: ['--timeout', '5000'], serial: false })
  })

  it.each([{ args: ['--failed-files-from=reports/run.json'] }, { args: ['--failed-files-from', 'reports/run.json'] }, { args: ['--failed-files', '--failed-files-from=reports/run.json'] }])('selects failed files from an explicit report: %j', ({ args }) => {
    expect(parseE2EOptions(args, 10)).toMatchObject({ failedFiles: true, failedFilesFrom: 'reports/run.json', playwrightArgs: [] })
  })

  it.each([{ args: ['--failed-files-from='] }, { args: ['--failed-files-from', ''] }])('rejects an empty report path: %j', ({ args }) => {
    expect(() => parseE2EOptions(args, 10)).toThrow('--failed-files-from option requires a nonempty report path')
  })

  it('rejects a repeated report path', () => {
    expect(() => parseE2EOptions(['--failed-files-from=one.json', '--failed-files-from=two.json'], 10)).toThrow('must appear only once')
  })

  it.each([
    { args: ['--failed-files', 'provider/file.spec.ts'], message: 'Remove the file arguments: provider/file.spec.ts' },
    { args: ['provider/file.spec.ts', '--failed-files-from=run.json'], message: 'Remove the file arguments: provider/file.spec.ts' },
    { args: ['--failed-files', '--', '--literal-filter'], message: 'Remove the file arguments: --literal-filter' },
    { args: ['--failed-files', '--last-failed'], message: 'Use one of the two options' },
    { args: ['--failed-files', '--test-list=list.txt'], message: 'Remove --test-list' },
    { args: ['--failed-files', '--only-changed'], message: 'Remove --only-changed' },
    { args: ['--failed-files', '--only-changed', 'main'], message: 'Remove --only-changed' },
  ])('refuses a selection that would not run the complete failed files: $args', ({ args, message }) => {
    expect(() => parseE2EOptions(args, 10)).toThrow(message)
  })

  it.each([
    { args: ['--failed-files', '--reporter', 'provider/file.spec.ts'] },
    { args: ['--failed-files', '--project', 'mock-chromium'] },
    { args: ['--failed-files', '--output', 'provider/results'] },
    { args: ['--failed-files', '--update-snapshots', 'all'] },
    { args: ['--failed-files', '--last-failed-file', 'state.json'] },
  ])('accepts an option value that resembles a file argument: $args', ({ args }) => {
    expect(parseE2EOptions(args, 10)).toMatchObject({ failedFiles: true, playwrightArgs: args.slice(1) })
  })

  it.each([
    { args: ['--failed-files', '--browser', 'chromium'] },
    { args: ['--failed-files', '--project', 'one', 'two'] },
    { args: ['--failed-files', '--project=one', 'two', 'three'] },
    { args: ['--failed-files', '--project', 'one', 'two', '--browser', 'chromium'] },
  ])('reads every value of a native option as a value, not as a file argument: $args', ({ args }) => {
    expect(parseE2EOptions(args, 10)).toMatchObject({ failedFiles: true, playwrightArgs: args.slice(1) })
  })

  it.each([
    { args: ['--failed-files', '--grep', 'title'], option: '--grep' },
    { args: ['--failed-files', '--grep=title'], option: '--grep' },
    { args: ['--failed-files', '-g', 'title'], option: '-g' },
    { args: ['--failed-files', '-gtitle'], option: '-g' },
    { args: ['--failed-files', '--grep-invert', 'title'], option: '--grep-invert' },
    { args: ['--failed-files', '-G', 'title'], option: '-G' },
    { args: ['--failed-files', '--test-list-invert', 'skip.txt'], option: '--test-list-invert' },
    { args: ['--failed-files', '--shard', '1/2'], option: '--shard' },
    { args: ['--failed-files-from=run.json', '--grep', 'title'], option: '--grep' },
  ])('refuses an option that selects fewer tests of the failed files: $args', ({ args, option }) => {
    expect(() => parseE2EOptions(args, 10)).toThrow(`reruns every test of the failed files. Remove ${option}`)
  })

  it('reads a narrowing option only as an option, never as the value of another option', () => {
    expect(parseE2EOptions(['--failed-files', '--reporter', '--grep'], 10)).toMatchObject({ failedFiles: true, reporters: '--grep' })
  })

  it.each([{ args: ['--grep', 'title'] }, { args: ['--shard=1/2'] }, { args: ['--test-list-invert', 'skip.txt'] }])('keeps a narrowing option valid without --failed-files: %j', ({ args }) => {
    expect(parseE2EOptions(args, 10).failedFiles).toBe(false)
  })
})

describe('parseE2EOptions native option classification', () => {
  interface NativeOption {
    readonly name: string
    readonly kind: 'flag' | 'value' | 'optional' | 'variadic'
  }

  /** Read the options of the installed `playwright test` command, which commander classifies by its own rules. */
  function nativeTestOptions(): NativeOption[] {
    const loaded: unknown = createRequire(import.meta.url)('playwright/lib/program')
    const program = isObject(loaded) ? loaded.program : undefined
    const commands = isObject(program) && Array.isArray(program.commands) ? program.commands : []
    const test = commands.find(command => isObject(command) && typeof command.name === 'function' && command.name() === 'test')
    if (!isObject(test) || !Array.isArray(test.options))
      throw new Error('The installed Playwright has no test command with an option list.')
    return test.options.map((option: unknown): NativeOption => {
      if (!isObject(option))
        throw new Error('The installed Playwright holds an option that is not an object.')
      const name = typeof option.long === 'string' ? option.long : option.short
      if (typeof name !== 'string')
        throw new Error('The installed Playwright holds an option without a name.')
      if (option.variadic === true)
        return { name, kind: 'variadic' }
      if (option.optional === true)
        return { name, kind: 'optional' }
      return { name, kind: option.required === true ? 'value' : 'flag' }
    })
  }

  /** The launcher refuses these together with --failed-files on purpose. Their own tests state the reason. */
  const REFUSED_WITH_FAILED_FILES = new Set(['--last-failed', '--test-list', '--test-list-invert', '--only-changed', '--grep', '--grep-invert', '--shard'])
  /** The launcher refuses these in every run. */
  const REFUSED_ALWAYS = new Set(['--fully-parallel'])
  /** The launcher validates the value of these options. */
  const VALID_VALUE: Readonly<Record<string, string>> = { '--workers': '2', '--retries': '0' }

  const classified = nativeTestOptions().filter(option => !REFUSED_WITH_FAILED_FILES.has(option.name) && !REFUSED_ALWAYS.has(option.name))

  it('reads the option list of the installed Playwright', () => {
    const names = nativeTestOptions().map(option => option.name)
    expect(names).toEqual(expect.arrayContaining(['--grep', '--project', '--only-changed', '--browser', '--headed']))
    for (const name of [...REFUSED_WITH_FAILED_FILES, ...REFUSED_ALWAYS])
      expect(names, `${name} must stay a native option`).toContain(name)
  })

  it.each(classified)('reads $name with the arity that Playwright gives it', (option) => {
    const value = VALID_VALUE[option.name] ?? 'value'
    const parse = (args: string[]) => () => parseE2EOptions(['--failed-files', ...args], 10)
    if (option.kind === 'flag') {
      // A flag takes no value, so the next argument is a file argument.
      expect(parse([option.name, 'sentinel.spec.ts'])).toThrow('Remove the file arguments: sentinel.spec.ts')
      return
    }
    // Every other kind takes the next argument as its value, so only the sentinel after it is a file argument.
    expect(parse([option.name, value])).not.toThrow()
    if (option.kind === 'variadic') {
      expect(parse([option.name, value, value, value])).not.toThrow()
      return
    }
    expect(parse([option.name, value, 'sentinel.spec.ts'])).toThrow(/Remove the file arguments: sentinel\.spec\.ts$/u)
  })

  it.each([...REFUSED_WITH_FAILED_FILES])('refuses %s together with --failed-files', (name) => {
    expect(() => parseE2EOptions(['--failed-files', name, '1/2'], 10)).toThrow('--failed-files')
  })
})

describe('shardSelectionArgs', () => {
  it.each(['--grep', '-g', '--grep-invert', '-G'])('preserves an output-like expression after %s', (option) => {
    expect(shardSelectionArgs([option, '--output=literal', '--reporter', 'json']))
      .toEqual([option, '--output=literal'])
  })

  it('removes reporter and output destinations without changing selection filters', () => {
    const args = ['--grep', 'pattern with spaces', '--reporter=json', '--output', 'a directory', 'provider/spec.ts', '--project=mock-chromium']
    expect(shardSelectionArgs(args)).toEqual(['--grep', 'pattern with spaces', 'provider/spec.ts', '--project=mock-chromium'])
  })

  it('preserves a literal output-like filter after the separator', () => {
    expect(shardSelectionArgs(['--', '--output=literal'])).toEqual(['--', '--output=literal'])
  })

  it('removes the last-run selection and keeps the file filter after the flag', () => {
    expect(shardSelectionArgs(['--last-failed', 'provider/spec.ts', '--last-failed-file', 'state dir/last.json', '--last-failed-file=other.json', '-g', 'title']))
      .toEqual(['provider/spec.ts', '-g', 'title'])
  })

  it('preserves a last-failed-like regular expression value', () => {
    expect(shardSelectionArgs(['--grep', '--last-failed', '-G', '--last-failed-file=x'])).toEqual(['--grep', '--last-failed', '-G', '--last-failed-file=x'])
  })

  it.each([
    { args: ['--only-changed', '--last-failed', 'a.spec.ts'], expected: ['a.spec.ts', '--only-changed'] },
    { args: ['--only-changed', '--reporter=json', 'a.spec.ts'], expected: ['a.spec.ts', '--only-changed'] },
    { args: ['--only-changed', '--output', 'dir', 'a.spec.ts'], expected: ['a.spec.ts', '--only-changed'] },
    { args: ['--only-changed', '--last-failed-file=x', '--grep', 'title'], expected: ['--grep', 'title', '--only-changed'] },
    { args: ['--only-changed', '--last-failed', '--', '--literal'], expected: ['--only-changed', '--', '--literal'] },
    { args: ['--only-changed'], expected: ['--only-changed'] },
  ])('keeps a file filter out of the value of a bare --only-changed: $args', ({ args, expected }) => {
    expect(shardSelectionArgs(args)).toEqual(expected)
  })

  it.each([
    { args: ['--only-changed', 'main', '--last-failed', 'a.spec.ts'], expected: ['--only-changed', 'main', 'a.spec.ts'] },
    { args: ['--only-changed=main', '--last-failed', 'a.spec.ts'], expected: ['--only-changed=main', 'a.spec.ts'] },
  ])('keeps the value that the caller gave --only-changed: $args', ({ args, expected }) => {
    expect(shardSelectionArgs(args)).toEqual(expected)
  })

  it.each([
    { args: ['--project', 'one', 'two', '--last-failed', 'a.spec.ts'], expected: ['a.spec.ts', '--project', 'one', 'two'] },
    { args: ['--project=one', '--reporter=json', 'a.spec.ts'], expected: ['a.spec.ts', '--project=one'] },
    { args: ['--project', 'one', 'two'], expected: ['--project', 'one', 'two'] },
  ])('keeps a file filter out of the project list: $args', ({ args, expected }) => {
    expect(shardSelectionArgs(args)).toEqual(expected)
  })
})

describe('serialRunArgs', () => {
  it.each(['--output=public output', '--output'])('moves an actual output option into this run: %s', (option) => {
    const args = option.includes('=') ? [option] : [option, 'public output']
    expect(serialRunArgs([...args, '--reporter', 'json', 'provider/file.spec.ts'], '/private run/test-results'))
      .toEqual(['--output=/private run/test-results', '--reporter', 'json', 'provider/file.spec.ts'])
  })

  it.each(['--grep', '-g', '--grep-invert', '-G'])('preserves an output-like expression after %s', (option) => {
    expect(serialRunArgs([option, '--output=literal', '--output=public'], '/private/test-results'))
      .toEqual(['--output=/private/test-results', option, '--output=literal'])
  })

  it('keeps custom configuration and explicit last-failed destinations', () => {
    expect(serialRunArgs(['-c', 'custom config.ts', '--last-failed-file', 'last run.json', '--last-failed'], '/private/test-results'))
      .toEqual(['--output=/private/test-results', '-c', 'custom config.ts', '--last-failed-file', 'last run.json', '--last-failed'])
  })

  it('preserves a literal output-like filter after the separator', () => {
    expect(serialRunArgs(['--', '--output=literal'], '/private/test-results'))
      .toEqual(['--output=/private/test-results', '--', '--output=literal'])
  })

  it('puts the launcher test list before the caller filters and their separator', () => {
    expect(serialRunArgs(['--grep', 'title', '--', '--literal'], '/private/test-results', '/private/failed files.txt'))
      .toEqual(['--output=/private/test-results', '--test-list=/private/failed files.txt', '--grep', 'title', '--', '--literal'])
  })

  it.each([
    { args: ['--debug', '--output=public', 'a.spec.ts'], expected: ['--output=/private/test-results', 'a.spec.ts', '--debug'] },
    { args: ['-u', '--output', 'public', 'a.spec.ts'], expected: ['--output=/private/test-results', 'a.spec.ts', '-u'] },
    { args: ['--update-snapshots', '--output=public', 'a.spec.ts'], expected: ['--output=/private/test-results', 'a.spec.ts', '--update-snapshots'] },
    { args: ['--debug', 'cli', '--output=public', 'a.spec.ts'], expected: ['--output=/private/test-results', '--debug', 'cli', 'a.spec.ts'] },
  ])('keeps a file filter out of the value of a bare optional option when it removes the output option: $args', ({ args, expected }) => {
    expect(serialRunArgs(args, '/private/test-results')).toEqual(expected)
  })
})

describe('discoveryRunArgs', () => {
  it('lists the caller selection with one worker and zero retries', () => {
    expect(discoveryRunArgs({ filters: ['--grep', 'title', 'provider/'] }))
      .toEqual(['--list', '--reporter=json', '--pass-with-no-tests', '--workers=1', '--retries=0', '--grep', 'title', 'provider/'])
  })

  it('reads the last-failed snapshot and the test list before the caller filters', () => {
    expect(discoveryRunArgs({ filters: ['--', '--literal'], lastFailedFile: '/run/last-failed.json', testList: '/run/failed files.txt' }))
      .toEqual(['--list', '--reporter=json', '--pass-with-no-tests', '--workers=1', '--retries=0', '--test-list=/run/failed files.txt', '--last-failed', '--last-failed-file=/run/last-failed.json', '--', '--literal'])
  })
})

describe('shardRunArgs', () => {
  it('selects a static shard with the native split', () => {
    expect(shardRunArgs({ filters: ['provider/'] }, { index: 2, total: 3 }))
      .toEqual(['--shard=2/3', '--workers=1', '--retries=0', '--reporter=list,blob,json', '--pass-with-no-tests', 'provider/'])
  })

  it('selects a balanced shard with its exact test list and no native split', () => {
    const args = shardRunArgs({ filters: ['-g', 'title'], testList: '/run/shard-1/test-list.txt', lastFailedFile: '/run/shard-1/last-run.json' })
    expect(args).toEqual(['--workers=1', '--retries=0', '--reporter=list,blob,json', '--pass-with-no-tests', '--test-list=/run/shard-1/test-list.txt', '--last-failed', '--last-failed-file=/run/shard-1/last-run.json', '-g', 'title'])
    expect(args.some(argument => argument.startsWith('--shard'))).toBe(false)
  })

  it('combines a static shard with the failed-file test list', () => {
    expect(shardRunArgs({ filters: [], testList: '/run/failed-files.txt' }, { index: 1, total: 2 }))
      .toEqual(['--shard=1/2', '--workers=1', '--retries=0', '--reporter=list,blob,json', '--pass-with-no-tests', '--test-list=/run/failed-files.txt'])
  })
})
