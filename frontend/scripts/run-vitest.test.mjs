import { spawn } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { launchVitest } from './run-vitest.mjs'

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal()
  const spawn = vi.fn()
  return {
    ...actual,
    spawn,
    default: { ...actual, spawn },
  }
})
vi.mock('node:process', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    default: {
      ...actual.default,
      versions: { ...actual.default.versions },
    },
  }
})

const nodeVersion = process.versions.node
const directories = []

beforeEach(() => {
  process.versions.node = nodeVersion
  vi.stubEnv('LEAPMUX_TEST_RESULTS_DIR', undefined)
  vi.mocked(spawn).mockReset().mockReturnValue(new EventEmitter())
})
afterEach(() => {
  vi.unstubAllEnvs()
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe('vitest launcher arguments', () => {
  it.each([
    { reporters: [] },
    {
      reporters: ['--reporter=verbose'],
    },
  ])('adds a JUnit report beside the console reporter: $reporters', ({ reporters }) => {
    const parent = resolve(import.meta.dirname, '../../.tmp')
    mkdirSync(parent, { recursive: true })
    const directory = mkdtempSync(join(parent, 'vitest-reports-'))
    directories.push(directory)
    vi.stubEnv('LEAPMUX_TEST_RESULTS_DIR', directory)
    launchVitest(['run', ...reporters])
    const args = vi.mocked(spawn).mock.calls[0][1]
    expect(args).toContain('--reporter=junit')
    expect(args).toContain(`--outputFile.junit=${join(directory, 'frontend.xml')}`)
    expect(args).toContain(reporters[0] ?? '--reporter=default')
  })
  it.each([
    { version: '24.0.0', inherited: undefined, expected: undefined },
    { version: '24.99.0', inherited: '--trace-warnings', expected: '--trace-warnings' },
    { version: '25.0.0', inherited: undefined, expected: '--no-experimental-webstorage' },
    { version: '26.0.0', inherited: '--trace-warnings', expected: '--trace-warnings --no-experimental-webstorage' },
  ])('selects the storage flag for Node $version and preserves inherited options', ({ version, inherited, expected }) => {
    process.versions.node = version
    vi.stubEnv('NODE_OPTIONS', inherited)
    launchVitest(['run'])
    expect(vi.mocked(spawn).mock.calls[0][2].env.NODE_OPTIONS).toBe(expected)
    expect(process.env.NODE_OPTIONS).toBe(inherited)
  })

  it.each([
    ['run', '--testNamePattern', 'a name with spaces'],
    ['run', '--testNamePattern', 'literal "$HOME" `command` $(>injected)'],
    ['run', 'a\\windows\\path.test.ts', '--reporter=verbose'],
    [],
  ].map(args => ({ args })))('passes arguments directly to the installed CLI: $args', ({ args }) => {
    launchVitest(args)
    expect(spawn).toHaveBeenCalledOnce()
    const [command, actualArgs, options] = vi.mocked(spawn).mock.calls[0]
    expect(command).toBe(process.execPath)
    expect(actualArgs[0]).toMatch(/[/\\]vitest[/\\]vitest\.mjs$/)
    expect(actualArgs.slice(1)).toEqual(args)
    expect(options.shell).not.toBe(true)
  })
})
