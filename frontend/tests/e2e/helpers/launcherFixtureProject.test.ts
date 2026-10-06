import type { Buffer } from 'node:buffer'
import type { FSWatcher } from 'node:fs'
import { ChildProcess, execFileSync } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isObject } from '../../../src/lib/jsonPick'
import { cleanupLauncherFixtureProject, createLauncherFixtureProject, fixtureCases, fixtureStringField, launcherFixtureDiagnostics, readFixtureRecord, readPlaywrightZipEntries, runLauncherFixtureProject, startLauncher, WAIT_FOR_FILE_MODULE } from './launcherFixtureProject'
import * as processHelpers from './process'

interface ArchiveBoundary {
  factory?: new (path: string) => { entries: () => Promise<string[]>, read: (entry: string) => Promise<Buffer>, close: () => void }
}

const boundary = vi.hoisted(() => {
  const archive: ArchiveBoundary = {}
  return {
    archive,
    watch: vi.fn<typeof import('node:fs').watch>(),
    close: vi.fn<typeof import('node:fs').closeSync>(),
    spawn: vi.fn<typeof import('node:child_process').spawn>(),
  }
})

vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>()
  boundary.watch.mockImplementation(actual.watch)
  boundary.close.mockImplementation(actual.closeSync)
  return { ...actual, watch: boundary.watch, closeSync: boundary.close }
})

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  boundary.spawn.mockImplementation(actual.spawn)
  return { ...actual, spawn: boundary.spawn }
})

vi.mock('node:module', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:module')>()
  return {
    ...actual,
    createRequire: (...args: Parameters<typeof actual.createRequire>) => new Proxy(actual.createRequire(...args), {
      apply: (target, receiver, argumentsList) => argumentsList[0] === 'playwright-core/lib/coreBundle' && boundary.archive.factory
        ? { utils: { ZipFile: boundary.archive.factory } }
        : Reflect.apply(target, receiver, argumentsList),
    }),
  }
})

const roots = new Set<string>()
afterEach(() => {
  for (const root of roots)
    rmSync(root, { recursive: true, force: true })
  roots.clear()
  delete boundary.archive.factory
  boundary.watch.mockClear()
  boundary.close.mockClear()
  boundary.spawn.mockClear()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function directory(): string {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'launcher-fixture-unit-'))
  roots.add(root)
  return root
}

/** The file wait of the generated project: the path, and the directory watch that the test controls. */
type ProjectWaitForFile = (path: string, watchDirectory: () => EventEmitter) => Promise<void>

/** Load the file wait from the module that a new fixture project holds, so the test reads the generated code. */
async function projectWaitForFile(): Promise<{ root: string, waitForFile: ProjectWaitForFile }> {
  const root = createLauncherFixtureProject()
  roots.add(root)
  const loaded: unknown = await import(pathToFileURL(join(root, WAIT_FOR_FILE_MODULE)).href)
  if (!isObject(loaded) || typeof loaded.waitForFile !== 'function')
    throw new Error('The fixture project holds no file wait.')
  const waitForFile = loaded.waitForFile
  return { root, waitForFile: (path, watchDirectory) => Reflect.apply(waitForFile, undefined, [path, watchDirectory]) }
}

/**
 * A directory watch that reports no event. A watcher whose FSEvents stream starts after a change reports none for it.
 * `close` counts the closes of the watcher.
 */
function silentWatch(): { watchDirectory: () => EventEmitter, watcher: EventEmitter, close: ReturnType<typeof vi.fn> } {
  const watcher = new EventEmitter()
  const close = vi.fn()
  Object.assign(watcher, { close })
  return { watchDirectory: () => watcher, watcher, close }
}

describe('cleanupLauncherFixtureProject', () => {
  it('retains the exact reports, console, and attachments of Playwright\'s own run after an outer failure', () => {
    const root = directory()
    const files = new Map([
      ['combined.json', '{"cases":[{"status":"timedOut","error":{"message":"The fixture case waits for beta."}}]}'],
      ['console.log', 'fixture alpha entered\nfixture alpha timed out\n'],
      ['receipt.txt', 'fixture-case-alpha\n'],
    ])
    for (const [file, content] of files)
      writeFileSync(join(root, file), content)

    cleanupLauncherFixtureProject(root, false)

    expect(existsSync(root), 'An outer failure must keep its complete evidence.').toBe(true)
    for (const [file, content] of files)
      expect(readFileSync(join(root, file), 'utf8')).toBe(content)
  })

  it('removes Playwright\'s own run that failed on purpose after its outer test succeeds', () => {
    const root = directory()
    writeFileSync(join(root, 'combined.json'), '{"stats":{"unexpected":1}}')

    cleanupLauncherFixtureProject(root, true)

    expect(existsSync(root)).toBe(false)
  })
})

describe('launcherFixtureDiagnostics', () => {
  it('keeps the exact error text of Playwright\'s own run and the report and console paths', () => {
    const root = directory()
    const result = { status: 'timedOut', errors: [{ message: 'The fixture alpha case waits for beta.\nFixture detail42', stack: 'fixture.mjs:55' }], attachments: [{ name: 'fixture-record', path: join(root, 'record.txt') }] }
    const cases = [{ id: 'fixture-alpha', title: 'executes alpha', file: 'alpha.spec.ts', status: 'unexpected', results: [result] }]
    const details = launcherFixtureDiagnostics({ root, records: join(root, 'records'), report: { errors: [{ message: 'fixture global error77' }] }, cases, code: 1, parallelRelease: false, reportPath: join(root, 'combined.json'), consolePath: join(root, 'console.log') })

    expect(JSON.parse(details)).toEqual({ root, reportPath: join(root, 'combined.json'), consolePath: join(root, 'console.log'), errors: [{ message: 'fixture global error77' }], cases })
    expect(details).toContain('Fixture detail42')
  })
})

describe('startLauncher', () => {
  it('preserves a synchronous spawn failure beside the log-close failure', async () => {
    const root = createLauncherFixtureProject()
    roots.add(root)
    const original = new Error('The controlled launcher cannot start.')
    const close = new Error('The controlled launcher log cannot close.')
    const actual = await vi.importActual<typeof import('node:fs')>('node:fs')
    boundary.spawn.mockImplementationOnce(() => {
      throw original
    })
    boundary.close.mockImplementationOnce((descriptor) => {
      actual.closeSync(descriptor)
      throw close
    })
    let failure: unknown
    try {
      startLauncher(root)
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The launcher discarded its start failure.')
    expect(failure.errors).toEqual([original, close])
  })

  it.each([false, true])('stops a spawned controller before reporting its log-close failure: stop fails %s', async (stopFails) => {
    const root = createLauncherFixtureProject()
    roots.add(root)
    const child = new ChildProcess()
    const close = new Error('The controlled launcher log cannot close.')
    const stop = new Error('The controlled launcher cannot stop.')
    const actual = await vi.importActual<typeof import('node:fs')>('node:fs')
    boundary.spawn.mockReturnValueOnce(child)
    boundary.close.mockImplementationOnce((descriptor) => {
      actual.closeSync(descriptor)
      throw close
    })
    const stopping = vi.spyOn(processHelpers, 'stopProcesses').mockImplementationOnce(async () => {
      if (stopFails)
        throw stop
      child.emit('close', 0, null)
    })
    let failure: unknown
    try {
      const launcher = startLauncher(root)
      await launcher.completion
    }
    catch (error) {
      failure = error
    }
    expect(stopping).toHaveBeenCalledWith([child])
    if (stopFails) {
      expect(failure).toBeInstanceOf(AggregateError)
      if (!(failure instanceof AggregateError))
        throw new Error('The launcher discarded its stop failure.')
      expect(failure.errors).toEqual([close, stop])
    }
    else {
      expect(failure).toBe(close)
    }
  })
})

describe('readFixtureRecord', () => {
  it('retains explicit false and zero fields in the records of Playwright\'s own run', () => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, '{"enabled":false,"count":0}')

    expect(readFixtureRecord(path)).toEqual({ enabled: false, count: 0 })
  })

  it.each(['null', '[]', '"not a record"'])('rejects a record of Playwright\'s own run that is not an object: %s', (content) => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, content)

    expect(() => readFixtureRecord(path)).toThrow('not an object')
  })

  it('returns the JSON error of a malformed record of Playwright\'s own run', () => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, '{')

    expect(() => readFixtureRecord(path)).toThrow(SyntaxError)
  })

  it('returns the file error of a missing record of Playwright\'s own run', () => {
    expect(() => readFixtureRecord(join(directory(), 'absent.json'))).toThrow('ENOENT')
  })
})

describe('fixtureStringField', () => {
  it('preserves a nonempty identity string of Playwright\'s own report', () => {
    expect(fixtureStringField({ id: 'fixture-owned-id' }, 'id')).toBe('fixture-owned-id')
  })

  it.each([undefined, null, '', 0, false, []])('rejects a missing or invalid identity field of Playwright\'s own report: %j', (id) => {
    expect(() => fixtureStringField({ id }, 'id')).toThrow('nonempty id')
  })
})

describe('fixtureCases', () => {
  const spec = { id: 'fixture-id', title: 'fixture case', file: 'fixture.spec.ts', tests: [{ status: 'expected', results: [] }] }

  it('reads the leaf suites of Playwright\'s own report that omit the child suite array', () => {
    expect(fixtureCases({ suites: [{ specs: [spec] }] })).toEqual([{ id: 'fixture-id', title: 'fixture case', file: 'fixture.spec.ts', status: 'expected', results: [] }])
  })

  it('accepts an empty report of Playwright\'s own run', () => {
    expect(fixtureCases({ suites: [] })).toEqual([])
  })

  it.each([
    {},
    { suites: null },
    { suites: [null] },
    { suites: [{ specs: [spec], suites: null }] },
    { suites: [{ specs: [{ ...spec, tests: [] }] }] },
    { suites: [{ specs: [{ ...spec, tests: [{ status: 'expected', results: null }] }] }] },
    { suites: [{ specs: [{ ...spec, id: '' }] }] },
  ])('rejects incomplete case records of Playwright\'s own report: %j', (report) => {
    expect(() => fixtureCases(report)).toThrow('fixture')
  })
})

describe('createLauncherFixtureProject', () => {
  it.each([false, true])('enables trace retention only for the explicit browser fixture: %s', (browserTrace) => {
    const root = createLauncherFixtureProject({ browserTrace })
    roots.add(root)
    const output = execFileSync(process.execPath, ['--input-type=module', '-e', 'import config from "./playwright.config.mjs"; process.stdout.write(JSON.stringify({ use: config.use, workers: config.workers, fullyParallel: config.fullyParallel }));'], { cwd: join(root, 'frontend'), encoding: 'utf8' })
    const config: unknown = JSON.parse(output)

    expect(config).toEqual({ use: browserTrace ? { browserName: 'chromium', headless: true, trace: 'retain-on-failure' } : {}, workers: 1, fullyParallel: false })
    expect(JSON.parse(readFileSync(join(root, 'records', 'policy.json'), 'utf8'))).toEqual({ browserTrace })
  })

  it('writes a file wait that ends at once for a file that exists, and stops watching', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const { root, waitForFile } = await projectWaitForFile()
    const path = join(root, 'records', 'release-alpha')
    writeFileSync(path, 'release')
    const watch = silentWatch()

    await waitForFile(path, watch.watchDirectory)

    expect(watch.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('writes a file wait that finds a file that appears with no watch event', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const { root, waitForFile } = await projectWaitForFile()
    const path = join(root, 'records', 'release-beta')
    const watch = silentWatch()
    let ended = false
    const waiting = waitForFile(path, watch.watchDirectory).then(() => {
      ended = true
    })
    await Promise.resolve()
    expect(ended).toBe(false)

    // The file appears after the first check, and the watcher reports nothing, as a watcher whose stream starts late.
    writeFileSync(path, 'release')
    vi.runOnlyPendingTimers()
    await waiting

    expect(ended).toBe(true)
    expect(watch.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('writes a file wait that fails with the error of its watcher, and stops checking', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const { root, waitForFile } = await projectWaitForFile()
    const watch = silentWatch()
    const failure = new Error('The controlled directory watch failed.')
    const waiting = waitForFile(join(root, 'records', 'release-build'), watch.watchDirectory)

    watch.watcher.emit('error', failure)

    await expect(waiting).rejects.toBe(failure)
    expect(watch.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('readPlaywrightZipEntries', () => {
  it('preserves both the read failure and the close failure of Playwright\'s own archive reader', async () => {
    const readFailure = new Error('The archive read failed.')
    const closeFailure = new Error('The archive close failed.')
    boundary.archive.factory = class {
      async entries() { return ['fixture.trace'] }
      async read(): Promise<Buffer> { throw readFailure }
      close() { throw closeFailure }
    }

    const failure = await readPlaywrightZipEntries(join(directory(), 'fixture.zip')).catch((error: unknown) => error)

    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The archive cleanup discarded the read failure.')
    expect(failure.errors).toEqual([readFailure, closeFailure])
  })

  it('returns the error of Playwright\'s own installed reader for an absent archive', async () => {
    await expect(readPlaywrightZipEntries(join(directory(), 'absent.zip'))).rejects.toThrow('ENOENT')
  })

  it('rejects a file that Playwright\'s own reader cannot read as a ZIP archive', async () => {
    const path = join(directory(), 'invalid.zip')
    writeFileSync(path, 'not a ZIP archive')

    await expect(readPlaywrightZipEntries(path)).rejects.toThrow()
  })
})

describe('runLauncherFixtureProject', () => {
  it('releases every held case of Playwright\'s own run when its owning watcher emits an error', async () => {
    const root = createLauncherFixtureProject()
    roots.add(root)
    const child = new ChildProcess()
    boundary.spawn.mockReturnValueOnce(child)
    const pending = runLauncherFixtureProject(root, { workers: 2 })
    const outcome = pending.then(() => undefined, (error: unknown) => error)
    const watcher: FSWatcher | undefined = boundary.watch.mock.results.at(-1)?.value
    if (!watcher)
      throw new Error('The fixture run started without its owning watcher.')
    const failure = new Error('The owned fixture watcher failed.')
    try {
      watcher.emit('error', failure)
      watcher.close()

      expect(existsSync(join(root, 'records', 'release-alpha'))).toBe(true)
      expect(existsSync(join(root, 'records', 'release-beta'))).toBe(true)
      expect(readFileSync(join(root, 'records', 'release-alpha'), 'utf8')).toBe('release')
      expect(readFileSync(join(root, 'records', 'release-beta'), 'utf8')).toBe('release')
    }
    finally {
      child.emit('close', 0, null)
      await outcome
    }
    expect(await outcome).toBe(failure)
  })
})
