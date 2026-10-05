import type { NativeLastRunState } from './e2eLastRunReporter'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import E2ELastRunReporter, { lastRunStatePath, readLastFailedState, writeNativeLastRunState } from './e2eLastRunReporter'

const roots: string[] = []
afterEach(() => {
  vi.unstubAllEnvs()
  for (const root of roots.splice(0))
    rmSync(root, { recursive: true, force: true })
})

function destination(): string {
  const scratch = resolve(import.meta.dirname, '../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'e2e-last-run-test-'))
  roots.push(root)
  return join(root, 'test-results', '.last-run.json')
}

function reporter(path: string): E2ELastRunReporter {
  vi.stubEnv('PLAYWRIGHT_LAST_RUN_OUTPUT_FILE', path)
  return new E2ELastRunReporter()
}

const statuses = {
  passed: 'passed',
  failed: 'failed',
  timedout: 'timedout',
  interrupted: 'interrupted',
} satisfies { [Status in NativeLastRunState['status']]: Status }

describe('E2ELastRunReporter', () => {
  it.each(Object.values(statuses))('preserves the native overall %s status', (status) => {
    const path = destination()
    const instance = reporter(path)
    const accepted = vi.fn(() => true)
    const rejected = vi.fn(() => false)
    instance.onBegin(undefined, { allTests: () => [{ id: 'accepted-native-id', ok: accepted }, { id: 'rejected-native-id', ok: rejected }] })

    instance.onEnd({ status })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status, failedTests: ['rejected-native-id'] })
    expect(accepted).toHaveBeenCalledTimes(1)
    expect(rejected).toHaveBeenCalledTimes(1)
    expect(instance.printsToStdio()).toBe(false)
  })

  it('reads native outcomes at run end instead of capturing the begin state', () => {
    const path = destination()
    const instance = reporter(path)
    const outcome = { accepted: true }
    instance.onBegin(undefined, { allTests: () => [{ id: 'settled-native-id', ok: () => outcome.accepted }] })
    outcome.accepted = false

    instance.onEnd({ status: 'failed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'failed', failedTests: ['settled-native-id'] })
  })

  it('preserves distinct native project and repeat IDs in their native order', () => {
    const path = destination()
    const instance = reporter(path)
    const ids = ['native-project-two-repeat-one', 'native-project-one-repeat-zero', 'native-project-two-repeat-zero']
    instance.onBegin(undefined, { allTests: () => ids.map(id => ({ id, ok: () => false })) })

    instance.onEnd({ status: 'failed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'failed', failedTests: ids })
  })

  it('preserves duplicate IDs just as the native last-run reporter does', () => {
    const path = destination()
    const instance = reporter(path)
    instance.onBegin(undefined, { allTests: () => [{ id: 'repeated-native-id', ok: () => false }, { id: 'repeated-native-id', ok: () => false }] })

    instance.onEnd({ status: 'failed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'failed', failedTests: ['repeated-native-id', 'repeated-native-id'] })
  })

  it('stores an empty failed selection when the native suite is empty', () => {
    const path = destination()
    const instance = reporter(path)
    instance.onBegin(undefined, { allTests: () => [] })

    instance.onEnd({ status: 'passed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'passed', failedTests: [] })
  })

  it('stores an empty failed selection when the native begin event is absent', () => {
    const path = destination()
    const instance = reporter(path)

    instance.onEnd({ status: 'failed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'failed', failedTests: [] })
  })

  it('uses the most recent native suite when a reporter instance receives another begin event', () => {
    const path = destination()
    const instance = reporter(path)
    instance.onBegin(undefined, { allTests: () => [{ id: 'older-native-id', ok: () => false }] })
    instance.onEnd({ status: 'failed' })
    instance.onBegin(undefined, { allTests: () => [{ id: 'newer-native-id', ok: () => true }] })

    instance.onEnd({ status: 'passed' })

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual({ status: 'passed', failedTests: [] })
  })

  it('propagates a native failure-predicate error before it replaces older state', () => {
    const path = destination()
    const prior: NativeLastRunState = { status: 'passed', failedTests: [] }
    writeNativeLastRunState(path, prior)
    const instance = reporter(path)
    const failure = new Error('The native outcome cannot be read.')
    instance.onBegin(undefined, { allTests: () => [{ id: 'native-id', ok: () => {
      throw failure
    } }] })

    expect(() => instance.onEnd({ status: 'failed' })).toThrow(failure)
    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(prior)
  })

  it('propagates the state write error to the native merge process', () => {
    const path = destination()
    mkdirSync(path, { recursive: true })
    const instance = reporter(path)
    instance.onBegin(undefined, { allTests: () => [] })

    expect(() => instance.onEnd({ status: 'passed' })).toThrow()
    expect(readdirSync(dirname(path))).toEqual(['.last-run.json'])
  })

  it.each([undefined, '', 'relative-last-run.json', `${resolve('native-last-run')}\0suffix`])('rejects an invalid native output destination: %j', (path) => {
    vi.stubEnv('PLAYWRIGHT_LAST_RUN_OUTPUT_FILE', path)

    expect(() => new E2ELastRunReporter()).toThrow('absolute path without NUL')
  })
})

describe('writeNativeLastRunState', () => {
  it('replaces the state with formatted native JSON and leaves no draft', () => {
    const path = destination()
    writeNativeLastRunState(path, { status: 'failed', failedTests: ['previous-native-id'] })

    writeNativeLastRunState(path, { status: 'passed', failedTests: [] })

    expect(readFileSync(path, 'utf8')).toBe(JSON.stringify({ status: 'passed', failedTests: [] }, null, 2))
    expect(readdirSync(dirname(path))).toEqual(['.last-run.json'])
  })

  it('rejects a relative destination before accessing the filesystem', () => {
    const mkdir = vi.fn<typeof mkdirSync>()

    expect(() => writeNativeLastRunState('relative-last-run.json', { status: 'passed', failedTests: [] }, {
      mkdirSync: mkdir,
      writeFileSync,
      renameSync,
      rmSync,
    })).toThrow('native last-run destination must be an absolute path without NUL')
    expect(mkdir).not.toHaveBeenCalled()
  })
})

describe('lastRunStatePath', () => {
  const cwd = resolve('frontend-root')
  const parent = resolve('frontend-root', 'test-results', '.last-run.json')

  it('uses the parent output root without an explicit destination', () => {
    expect(lastRunStatePath(undefined, {}, cwd, parent)).toBe(parent)
  })

  it('treats an empty environment value as absent, as native Playwright does', () => {
    expect(lastRunStatePath(undefined, { PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: '' }, cwd, parent)).toBe(parent)
  })

  it('resolves a relative environment destination against the native working directory', () => {
    expect(lastRunStatePath(undefined, { PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: 'state/last.json' }, cwd, parent)).toBe(join(cwd, 'state', 'last.json'))
  })

  it('gives --last-failed-file precedence over the environment destination', () => {
    const explicit = resolve('elsewhere', 'last.json')
    expect(lastRunStatePath(explicit, { PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: 'ignored.json' }, cwd, parent)).toBe(explicit)
    expect(lastRunStatePath('relative.json', { PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: 'ignored.json' }, cwd, parent)).toBe(join(cwd, 'relative.json'))
  })
})

describe('readLastFailedState', () => {
  it('returns the failed IDs and the exact bytes of the state file', () => {
    const path = destination()
    const content = '{\n  "status": "failed",\n  "failedTests": ["one", "two", "one"]\n}'
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, content)

    expect(readLastFailedState(path)).toEqual({ failedTests: ['one', 'two', 'one'], content })
  })

  it('accepts a state that lists no failed test', () => {
    const path = destination()
    writeNativeLastRunState(path, { status: 'passed', failedTests: [] })

    expect(readLastFailedState(path).failedTests).toEqual([])
  })

  it('refuses an absent state instead of selecting every test', () => {
    const path = destination()

    expect(() => readLastFailedState(path)).toThrow(`reads the last-run state at ${path}, but that file does not exist`)
  })

  it('refuses an unreadable state and keeps the read error as its cause', () => {
    const path = destination()
    mkdirSync(path, { recursive: true })

    expect(() => readLastFailedState(path)).toThrow(expect.objectContaining({
      message: `The --last-failed option cannot read the last-run state at ${path}.`,
      cause: expect.objectContaining({ code: 'EISDIR' }),
    }))
  })

  it('refuses invalid JSON and keeps the parse error as its cause', () => {
    const path = destination()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, '{"failedTests": [')

    expect(() => readLastFailedState(path)).toThrow(expect.objectContaining({ message: expect.stringContaining('is not valid JSON'), cause: expect.any(SyntaxError) }))
  })

  it.each([
    { label: 'an array', value: [] },
    { label: 'null', value: null },
    { label: 'an absent ID list', value: { status: 'failed' } },
    { label: 'a string ID list', value: { failedTests: 'one' } },
    { label: 'a numeric ID', value: { failedTests: ['one', 2] } },
    { label: 'an empty ID', value: { failedTests: [''] } },
  ])('refuses a state with $label', ({ value }) => {
    const path = destination()
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, JSON.stringify(value))

    expect(() => readLastFailedState(path)).toThrow('has no failedTests array of test IDs')
  })
})
