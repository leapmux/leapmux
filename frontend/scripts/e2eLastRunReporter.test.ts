import type { NativeLastRunState } from './e2eLastRunReporter'
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import E2ELastRunReporter, { writeNativeLastRunState } from './e2eLastRunReporter'

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
  it('keeps the preceding state complete until the draft is renamed', () => {
    const path = destination()
    const prior: NativeLastRunState = { status: 'failed', failedTests: ['previous-native-id'] }
    const next: NativeLastRunState = { status: 'passed', failedTests: [] }
    writeNativeLastRunState(path, prior)
    const observed: string[] = []

    writeNativeLastRunState(path, next, {
      mkdirSync,
      writeFileSync: (draft, content, options) => {
        expect(typeof draft).toBe('string')
        if (typeof draft !== 'string')
          throw new Error('The native state draft is not a file path.')
        observed.push(draft)
        expect(dirname(draft)).toBe(dirname(path))
        expect(basename(draft)).toMatch(/^\.leapmux-last-run-[0-9a-f-]+\.writing$/u)
        expect(options).toEqual({ encoding: 'utf8', flag: 'wx', mode: 0o600 })
        expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(prior)
        writeFileSync(draft, content, options)
      },
      renameSync: (draft, target) => {
        expect(target).toBe(path)
        expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(prior)
        expect(JSON.parse(readFileSync(draft, 'utf8'))).toEqual(next)
        renameSync(draft, target)
      },
      rmSync,
    })

    expect(observed).toHaveLength(1)
    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(next)
    expect(readdirSync(dirname(path))).toEqual(['.last-run.json'])
  })

  it('removes a partial draft after a write failure and preserves the preceding state', () => {
    const path = destination()
    const prior: NativeLastRunState = { status: 'failed', failedTests: ['previous-native-id'] }
    writeNativeLastRunState(path, prior)
    const failure = new Error('The native state write failed.')

    expect(() => writeNativeLastRunState(path, { status: 'passed', failedTests: [] }, {
      mkdirSync,
      writeFileSync: (draft) => {
        writeFileSync(draft, '{partial')
        throw failure
      },
      renameSync,
      rmSync,
    })).toThrow(failure)

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(prior)
    expect(readdirSync(dirname(path))).toEqual(['.last-run.json'])
  })

  it('removes the complete draft after a rename failure and preserves the preceding state', () => {
    const path = destination()
    const prior: NativeLastRunState = { status: 'failed', failedTests: ['previous-native-id'] }
    writeNativeLastRunState(path, prior)
    const failure = new Error('The native state rename failed.')

    expect(() => writeNativeLastRunState(path, { status: 'passed', failedTests: [] }, {
      mkdirSync,
      writeFileSync,
      renameSync: () => {
        throw failure
      },
      rmSync,
    })).toThrow(failure)

    expect(JSON.parse(readFileSync(path, 'utf8'))).toEqual(prior)
    expect(readdirSync(dirname(path))).toEqual(['.last-run.json'])
  })

  it('reports both write and draft-cleanup failures', () => {
    const path = destination()
    const writeFailure = new Error('The native state write failed.')
    const cleanupFailure = new Error('The native state draft cleanup failed.')
    let observed: unknown
    try {
      writeNativeLastRunState(path, { status: 'passed', failedTests: [] }, {
        mkdirSync,
        writeFileSync: () => {
          throw writeFailure
        },
        renameSync,
        rmSync: () => {
          throw cleanupFailure
        },
      })
    }
    catch (error) {
      observed = error
    }

    expect(observed).toBeInstanceOf(AggregateError)
    if (!(observed instanceof AggregateError))
      throw new Error('The native state writer discarded one of its failures.')
    expect(observed.errors).toEqual([writeFailure, cleanupFailure])
  })

  it('propagates a directory failure before creating a draft', () => {
    const path = destination()
    const failure = new Error('The native state directory cannot be created.')
    const write = vi.fn<typeof writeFileSync>()

    expect(() => writeNativeLastRunState(path, { status: 'passed', failedTests: [] }, {
      mkdirSync: () => {
        throw failure
      },
      writeFileSync: write,
      renameSync,
      rmSync,
    })).toThrow(failure)
    expect(write).not.toHaveBeenCalled()
  })

  it('rejects a relative destination before accessing the filesystem', () => {
    const mkdir = vi.fn<typeof mkdirSync>()

    expect(() => writeNativeLastRunState('relative-last-run.json', { status: 'passed', failedTests: [] }, {
      mkdirSync: mkdir,
      writeFileSync,
      renameSync,
      rmSync,
    })).toThrow('absolute path without NUL')
    expect(mkdir).not.toHaveBeenCalled()
  })
})
