import type { Buffer } from 'node:buffer'
import type { FSWatcher } from 'node:fs'
import { ChildProcess, execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanupNativeE2eFixture, createNativeE2eFixture, executeNativeE2eFixture, nativeFixtureCases, nativeFixtureDiagnostics, nativeFixtureStringField, readNativeFixtureRecord, readNativeZipEntries, startNativeE2eRunner } from './nativeE2eFixture'
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
})

function directory(): string {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  const root = mkdtempSync(join(scratch, 'native-e2e-fixture-unit-'))
  roots.add(root)
  return root
}

describe('cleanupNativeE2eFixture', () => {
  it('retains exact native reports, console, and attachments after an outer failure', () => {
    const root = directory()
    const files = new Map([
      ['combined.json', '{"cases":[{"status":"timedOut","error":{"message":"The native case waits for beta."}}]}'],
      ['console.log', 'native alpha entered\nnative alpha timed out\n'],
      ['receipt.txt', 'native-case-alpha\n'],
    ])
    for (const [file, content] of files)
      writeFileSync(join(root, file), content)

    cleanupNativeE2eFixture(root, false)

    expect(existsSync(root), 'An outer failure must preserve its complete native evidence.').toBe(true)
    for (const [file, content] of files)
      expect(readFileSync(join(root, file), 'utf8')).toBe(content)
  })

  it('removes an intentionally failed native run after its outer test succeeds', () => {
    const root = directory()
    writeFileSync(join(root, 'combined.json'), '{"stats":{"unexpected":1}}')

    cleanupNativeE2eFixture(root, true)

    expect(existsSync(root)).toBe(false)
  })
})

describe('nativeFixtureDiagnostics', () => {
  it('keeps exact native error text and the report and console paths', () => {
    const root = directory()
    const result = { status: 'timedOut', errors: [{ message: 'The native alpha case waits for beta.\nNative detail42', stack: 'native fixture.mjs:55' }], attachments: [{ name: 'native-record', path: join(root, 'record.txt') }] }
    const cases = [{ id: 'native-alpha', title: 'executes alpha', file: 'alpha.spec.ts', status: 'unexpected', results: [result] }]
    const details = nativeFixtureDiagnostics({ root, records: join(root, 'records'), report: { errors: [{ message: 'native global error77' }] }, cases, code: 1, parallelRelease: false, reportPath: join(root, 'combined.json'), consolePath: join(root, 'console.log') })

    expect(JSON.parse(details)).toEqual({ root, reportPath: join(root, 'combined.json'), consolePath: join(root, 'console.log'), errors: [{ message: 'native global error77' }], cases })
    expect(details).toContain('Native detail42')
  })
})

describe('startNativeE2eRunner', () => {
  it('preserves a synchronous spawn failure beside the log-close failure', async () => {
    const root = createNativeE2eFixture()
    roots.add(root)
    const original = new Error('The controlled native controller cannot start.')
    const close = new Error('The controlled native log cannot close.')
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
      startNativeE2eRunner(root)
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The native controller discarded its start failure.')
    expect(failure.errors).toEqual([original, close])
  })

  it.each([false, true])('stops a spawned controller before reporting its log-close failure: stop fails %s', async (stopFails) => {
    const root = createNativeE2eFixture()
    roots.add(root)
    const child = new ChildProcess()
    const close = new Error('The controlled native log cannot close.')
    const stop = new Error('The controlled native controller cannot stop.')
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
      const runner = startNativeE2eRunner(root)
      await runner.completion
    }
    catch (error) {
      failure = error
    }
    expect(stopping).toHaveBeenCalledWith([child])
    if (stopFails) {
      expect(failure).toBeInstanceOf(AggregateError)
      if (!(failure instanceof AggregateError))
        throw new Error('The native controller discarded its stop failure.')
      expect(failure.errors).toEqual([close, stop])
    }
    else {
      expect(failure).toBe(close)
    }
  })
})

describe('readNativeFixtureRecord', () => {
  it('retains explicit false and zero fields in native records', () => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, '{"enabled":false,"count":0}')

    expect(readNativeFixtureRecord(path)).toEqual({ enabled: false, count: 0 })
  })

  it.each(['null', '[]', '"not a record"'])('rejects a non-object native record: %s', (content) => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, content)

    expect(() => readNativeFixtureRecord(path)).toThrow('not an object')
  })

  it('returns a malformed native JSON error', () => {
    const path = join(directory(), 'record.json')
    writeFileSync(path, '{')

    expect(() => readNativeFixtureRecord(path)).toThrow(SyntaxError)
  })

  it('returns a missing native file error', () => {
    expect(() => readNativeFixtureRecord(join(directory(), 'absent.json'))).toThrow('ENOENT')
  })
})

describe('nativeFixtureStringField', () => {
  it('preserves a nonempty native identity string', () => {
    expect(nativeFixtureStringField({ id: 'native-owned-id' }, 'id')).toBe('native-owned-id')
  })

  it.each([undefined, null, '', 0, false, []])('rejects a missing or invalid native identity field: %j', (id) => {
    expect(() => nativeFixtureStringField({ id }, 'id')).toThrow('nonempty id')
  })
})

describe('nativeFixtureCases', () => {
  const spec = { id: 'native-id', title: 'native case', file: 'native.spec.ts', tests: [{ status: 'expected', results: [] }] }

  it('reads native leaf suites that omit the child suite array', () => {
    expect(nativeFixtureCases({ suites: [{ specs: [spec] }] })).toEqual([{ id: 'native-id', title: 'native case', file: 'native.spec.ts', status: 'expected', results: [] }])
  })

  it('accepts an empty native report', () => {
    expect(nativeFixtureCases({ suites: [] })).toEqual([])
  })

  it.each([
    {},
    { suites: null },
    { suites: [null] },
    { suites: [{ specs: [spec], suites: null }] },
    { suites: [{ specs: [{ ...spec, tests: [] }] }] },
    { suites: [{ specs: [{ ...spec, tests: [{ status: 'expected', results: null }] }] }] },
    { suites: [{ specs: [{ ...spec, id: '' }] }] },
  ])('rejects incomplete native case records: %j', (report) => {
    expect(() => nativeFixtureCases(report)).toThrow('native')
  })
})

describe('createNativeE2eFixture', () => {
  it.each([false, true])('enables trace retention only for the explicit browser fixture: %s', (browserTrace) => {
    const root = createNativeE2eFixture({ browserTrace })
    roots.add(root)
    const output = execFileSync(process.execPath, ['--input-type=module', '-e', 'import config from "./playwright.config.mjs"; process.stdout.write(JSON.stringify({ use: config.use, workers: config.workers, fullyParallel: config.fullyParallel }));'], { cwd: join(root, 'frontend'), encoding: 'utf8' })
    const config: unknown = JSON.parse(output)

    expect(config).toEqual({ use: browserTrace ? { browserName: 'chromium', headless: true, trace: 'retain-on-failure' } : {}, workers: 1, fullyParallel: false })
    expect(JSON.parse(readFileSync(join(root, 'records', 'policy.json'), 'utf8'))).toEqual({ browserTrace })
  })
})

describe('readNativeZipEntries', () => {
  it('preserves both native archive read and archive close failures', async () => {
    const readFailure = new Error('The native archive read failed.')
    const closeFailure = new Error('The native archive close failed.')
    boundary.archive.factory = class {
      async entries() { return ['native.trace'] }
      async read(): Promise<Buffer> { throw readFailure }
      close() { throw closeFailure }
    }

    const failure = await readNativeZipEntries(join(directory(), 'native.zip')).catch((error: unknown) => error)

    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The archive cleanup discarded the read failure.')
    expect(failure.errors).toEqual([readFailure, closeFailure])
  })

  it('returns the installed native reader error for an absent archive', async () => {
    await expect(readNativeZipEntries(join(directory(), 'absent.zip'))).rejects.toThrow('ENOENT')
  })

  it('rejects a file that is not a native ZIP archive', async () => {
    const path = join(directory(), 'invalid.zip')
    writeFileSync(path, 'not a ZIP archive')

    await expect(readNativeZipEntries(path)).rejects.toThrow()
  })
})

describe('executeNativeE2eFixture', () => {
  it('releases every held native case when its owning watcher emits an error', async () => {
    const root = createNativeE2eFixture()
    roots.add(root)
    const child = new ChildProcess()
    boundary.spawn.mockReturnValueOnce(child)
    const pending = executeNativeE2eFixture(root, { workers: 2 })
    const outcome = pending.then(() => undefined, (error: unknown) => error)
    const watcher: FSWatcher | undefined = boundary.watch.mock.results.at(-1)?.value
    if (!watcher)
      throw new Error('The native fixture started without its owning watcher.')
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
