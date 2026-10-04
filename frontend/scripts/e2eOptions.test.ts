import { afterEach, describe, expect, it, vi } from 'vitest'
import { parseE2EOptions, serialRunArgs, shardSelectionArgs } from './e2eOptions'

afterEach(() => vi.unstubAllEnvs())

describe('parseE2EOptions', () => {
  it('limits the default shard count to four and the available CPU capacity', () => {
    expect(parseE2EOptions([], 10).workers).toBe(4)
    expect(parseE2EOptions([], 2).workers).toBe(2)
    expect(parseE2EOptions([], 1).serial).toBe(true)
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

  it.each(['--headed', '--debug', '--ui', '--list', '--last-failed', '--last-failed-file=one.json', '--shard=1/3', '--config=other.ts', '--max-failures=1', '-x', '--global-timeout=1000'])('keeps interactive and externally selected runs serial: %s', (flag) => {
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

  it('keeps an explicit environment destination for the last run serial', () => {
    vi.stubEnv('PLAYWRIGHT_LAST_RUN_OUTPUT_FILE', 'caller-last-run.json')
    expect(parseE2EOptions(['--workers=3'], 10)).toMatchObject({ workers: 1, serial: true })
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
})
