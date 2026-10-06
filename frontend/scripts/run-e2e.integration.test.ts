import type { Buffer } from 'node:buffer'
import type { FSWatcher } from 'node:fs'
import type { Mock } from 'vitest'
import type { FixturePolicy, FixtureRecord, FixtureRun, LauncherCompletion } from '../tests/e2e/helpers/launcherFixtureProject'
import { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, readdirSync, readFileSync, watch, writeFileSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isObject } from '../src/lib/jsonPick'
import { withCleanup } from '../tests/e2e/helpers/cleanup'
import { cleanupLauncherFixtureProject, createLauncherFixtureProject, fixtureStringField, launcherFixtureDiagnostics, readFixtureRecord, releaseFixtureProcesses, runLauncherFixtureProject, startLauncher } from '../tests/e2e/helpers/launcherFixtureProject'
import * as processHelpers from '../tests/e2e/helpers/process'
import * as processRegistry from '../tests/e2e/helpers/processRegistry'

// A test replaces one watch with a watcher that reports no event: the case of a file that appears before the FSEvents
// stream of the watcher starts. Every other watch is the real one.
vi.mock('node:fs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:fs')>()
  return { ...actual, watch: vi.fn(actual.watch) }
})

/** The time between two existence checks of `waitForFixtureFiles`, in milliseconds. It never sets when the wait ends. */
const RECORD_CHECK_INTERVAL_MS = 50
const INTEGRATION_DEADLINE_MS = 30_000
const CANCELLATION_PHASE_DEADLINE_MS = 15_000
const CANCELLATION_CLEANUP_DEADLINE_MS = 10_000
const CANCELLATION_TEST_DEADLINE_MS = 45_000
const CANCELLATION_CASES = [
  { signal: 'SIGINT' as const, code: 130 },
  { signal: 'SIGTERM' as const, code: 143 },
]
const fixtures = new Set<string>()

afterEach((context) => {
  const passed = context.task.result?.state === 'pass'
  for (const fixture of fixtures) {
    if (!passed)
      console.info('Launcher fixture failure evidence directory:', fixture)
    cleanupLauncherFixtureProject(fixture, passed)
  }
  fixtures.clear()
  vi.restoreAllMocks()
  // Drop a silent watcher that a failed test did not consume, and restore the real watch.
  vi.mocked(watch).mockReset()
  vi.useRealTimers()
  vi.unstubAllEnvs()
})

/** A directory watcher that reports no event, as a watch does when the file appears before its stream starts. */
type SilentWatcher = FSWatcher & { close: Mock<() => void> }

/** Make the next watch report no event, and return that watcher. */
function silenceNextWatch(): SilentWatcher {
  const watcher = Object.assign(new EventEmitter(), { close: vi.fn<() => void>(), ref: vi.fn(), unref: vi.fn() }) as unknown as SilentWatcher
  vi.mocked(watch).mockImplementationOnce(() => watcher)
  return watcher
}

function createFixture(policy: FixturePolicy = {}): string {
  const root = createLauncherFixtureProject(policy)
  fixtures.add(root)
  return root
}

interface RepeatedFixture {
  root: string
  args?: string[]
  reportPath: string
}

function executeFixture(workers: 1 | 2, failCases?: readonly string[], repeated?: RepeatedFixture): Promise<FixtureRun> {
  const root = repeated?.root ?? createFixture(failCases === undefined ? {} : { failCases })
  return runLauncherFixtureProject(root, {
    workers,
    ...(failCases === undefined ? {} : { failCases }),
    ...(repeated?.args === undefined ? {} : { args: repeated.args }),
    ...(repeated?.reportPath === undefined ? {} : { reportPath: repeated.reportPath }),
  })
}

function startPrivateRunner(root: string): ReturnType<typeof startLauncher> {
  return startLauncher(root)
}

/**
 * Wait until the fixture writes each of the files into the records directory.
 *
 * The wait checks for the files when it starts, on each watch event, and every RECORD_CHECK_INTERVAL_MS, as
 * `waitForFile` in the fixture project does. A directory watch alone cannot end the wait. macOS starts the FSEvents
 * stream of a watcher after watch() returns, so a file that appears in that window gives no event. The interval check
 * finds such a file, so the wait does not depend on the start order of the processes. The watch only ends the wait
 * sooner.
 */
function waitForFixtureFiles(records: string, files: string[], completion: Promise<LauncherCompletion>, signal: AbortSignal): Promise<void> {
  return new Promise((accept, reject) => {
    let finished = false
    const listener = watch(records, check)
    const timer = setInterval(check, RECORD_CHECK_INTERVAL_MS)
    const finish = (error?: unknown) => {
      if (finished)
        return
      finished = true
      clearInterval(timer)
      listener.close()
      signal.removeEventListener('abort', abort)
      if (error === undefined)
        accept()
      else
        reject(error)
    }
    function abort() {
      finish(new Error(`The fixture did not create ${files.join(', ')} before the cancellation deadline.`))
    }
    function check() {
      if (files.every(file => existsSync(join(records, file))))
        finish()
    }
    listener.once('error', finish)
    signal.addEventListener('abort', abort, { once: true })
    void completion.then(() => finish(new Error(`The private runner exited before it created ${files.join(', ')}.`)), finish)
    if (signal.aborted)
      abort()
    else
      check()
  })
}

function waitForRunnerCompletion(completion: Promise<LauncherCompletion>, signal: AbortSignal): Promise<LauncherCompletion> {
  return new Promise((accept, reject) => {
    const abort = () => reject(new Error('The private runner did not exit before the cancellation deadline.'))
    signal.addEventListener('abort', abort, { once: true })
    void completion.then(accept, reject).finally(() => signal.removeEventListener('abort', abort))
    if (signal.aborted)
      abort()
  })
}

function requireProcessExit(record: FixtureRecord, key: string): void {
  const pid = record[key]
  if (typeof pid !== 'number' || !Number.isSafeInteger(pid) || pid <= 0)
    throw new Error(`The fixture record has no valid ${key}.`)
  expect(() => process.kill(pid, 0)).toThrow(expect.objectContaining({ code: 'ESRCH' }))
}

function cancelPrivateRunner(child: ChildProcess, signal: NodeJS.Signals): void {
  if (process.platform === 'win32') {
    if (!child.stdin)
      throw new Error('The private runner has no signal control pipe.')
    child.stdin.end(`${signal}\n`)
  }
  else {
    expect(child.kill(signal)).toBe(true)
  }
}

async function stopPrivateRunner(root: string, records: string, runner: ReturnType<typeof startPrivateRunner>): Promise<void> {
  releaseFixtureProcesses(records)
  await withCleanup(async () => {
    await processHelpers.stopProcesses([runner.child], CANCELLATION_CLEANUP_DEADLINE_MS)
    await runner.completion
  }, () => stopFixtureDescendants(root, records))
}

/** Stop owned fixture descendants after an assertion exposes a runner cleanup defect. */
async function stopFixtureDescendants(root: string, records: string): Promise<void> {
  const directory = join(root, 'fixture-cleanup', 'processes')
  mkdirSync(directory, { recursive: true })
  const pids = new Set<number>()
  for (const file of readdirSync(records)) {
    let fields: string[] = []
    if (file.startsWith('setup-') && file.endsWith('.json'))
      fields = ['processId']
    else if (/^entry-(?:alpha|beta)\.json$/u.test(file))
      fields = ['workerProcessId', 'ownedChildProcessId']
    else if (/^owned-child-(?:alpha|beta)\.json$/u.test(file))
      fields = ['ownedChildProcessId']
    else if (file === 'build-entry.json')
      fields = ['processId', 'parentProcessId']
    if (fields.length === 0)
      continue
    const record = readFixtureRecord(join(records, file))
    for (const field of fields) {
      const pid = record[field]
      if (typeof pid === 'number' && Number.isSafeInteger(pid) && pid > 1 && pid !== process.pid)
        pids.add(pid)
    }
  }
  for (const pid of pids)
    writeFileSync(join(directory, String(pid)), '')
  await processRegistry.stopTrackedProcesses(join(root, 'fixture-cleanup'))
}

function requireFixtureCleanupAndArtifacts(run: FixtureRun, setupCount: number): void {
  expect(readFileSync(join(run.root, 'build-count'), 'utf8')).toBe('1')
  const setupFiles = readdirSync(run.records).filter(file => file.startsWith('setup-'))
  const teardownFiles = readdirSync(run.records).filter(file => file.startsWith('teardown-'))
  expect(setupFiles).toHaveLength(setupCount)
  expect(teardownFiles).toHaveLength(setupCount)
  const states = setupFiles.map(file => readFixtureRecord(join(run.records, file)))
  const teardowns = teardownFiles.map(file => readFixtureRecord(join(run.records, file)))
  expect(new Set(states.map(state => fixtureStringField(state, 'runDir'))).size).toBe(setupCount)
  expect(new Set(states.map(state => fixtureStringField(state, 'nonce'))).size).toBe(setupCount)
  expect(new Set(states.map(state => fixtureStringField(state, 'noncePath'))).size).toBe(setupCount)
  expect(new Set(states.map(state => fixtureStringField(state, 'statePath'))).size).toBe(setupCount)
  expect(new Set(states.map(state => state.processId)).size).toBe(setupCount)
  expect(new Set(states.map(state => state.port)).size).toBe(setupCount)
  for (const state of states) {
    expect(state.workers).toBe(1)
    const runDir = fixtureStringField(state, 'runDir')
    expect(state.runDirIsSymlink).toBe(false)
    const runRoot = setupCount === 1 ? runDir : dirname(runDir)
    expect(basename(runRoot)).toMatch(/^e-[A-Za-z0-9]{6}$/)
    if (setupCount > 1)
      expect(basename(runDir)).toMatch(/^[1-9]\d*$/)
    const inside = relative(join(run.root, '.tmp'), runDir)
    expect(isAbsolute(inside)).toBe(false)
    expect(inside === '..' || inside.startsWith(`..${sep}`)).toBe(false)
    expect(dirname(fixtureStringField(state, 'binaryPath'))).toBe(runDir)
    expect(dirname(fixtureStringField(state, 'noncePath'))).toBe(runDir)
    expect(existsSync(runDir)).toBe(false)
    expect(existsSync(fixtureStringField(state, 'binaryPath'))).toBe(false)
    expect(teardowns).toContainEqual({ nonce: state.nonce, port: state.port })
  }
  expect(readdirSync(join(run.root, '.tmp'))).toEqual([])
  expect(run.report.errors).toEqual([])
  expect(run.cases.map(test => test.title)).toEqual(['executes alpha', 'executes beta'])
  expect(new Set(run.cases.map(test => test.file)).size).toBe(2)
  for (const test of run.cases) {
    expect(test.results).toHaveLength(1)
    const result = test.results[0]
    if (!isObject(result) || !Array.isArray(result.attachments))
      throw new Error('The fixture result has no attachment array.')
    const attachments = result.attachments.filter(attachment => isObject(attachment) && attachment.name === 'isolation-receipt')
    expect(attachments).toHaveLength(1)
    const attachment = attachments[0]
    if (!isObject(attachment))
      throw new Error('The fixture result has no isolation receipt.')
    const path = fixtureStringField(attachment, 'path')
    const retainedPath = isAbsolute(path) ? path : resolve(run.root, 'frontend', path)
    expect(readFileSync(retainedPath, 'utf8')).toBe(`fixture-case-${test.title.slice('executes '.length)}`)
  }
  for (const label of ['alpha', 'beta']) {
    const entry = readFixtureRecord(join(run.records, `entry-${label}.json`))
    expect(entry.parallelIndex).toBe(0)
    expect(states.map(state => state.nonce)).toContain(entry.nonce)
    expect(states.map(state => state.port)).toContain(entry.port)
  }
}

function requireAllRunDirsRemoved(root: string): void {
  const records = join(root, 'records')
  for (const file of readdirSync(records).filter(file => file.startsWith('setup-') && file.endsWith('.json'))) {
    const state = readFixtureRecord(join(records, file))
    expect(existsSync(fixtureStringField(state, 'runDir'))).toBe(false)
  }
  expect(readdirSync(join(root, '.tmp'))).toEqual([])
}

/** Save each preceding run's evidence before another command uses the same output root. */
function retainedArtifactFiles(root: string): Map<string, Buffer> {
  const files = new Map<string, Buffer>()
  const visit = (directory: string): void => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory())
        visit(path)
      else if (entry.isFile())
        files.set(path, readFileSync(path))
      else
        throw new Error('The retained evidence contains an unexpected filesystem entry.')
    }
  }
  visit(join(root, 'retained-artifacts', 'runs'))
  return files
}

describe('waitForFixtureFiles', () => {
  /** Start a wait on a watch that reports no event. The test advances the interval check itself. */
  function silentWait(files: string[], completion: Promise<LauncherCompletion> = new Promise(() => {})) {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const records = join(createFixture(), 'records')
    const watcher = silenceNextWatch()
    const wait = waitForFixtureFiles(records, files, completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
    return { records, watcher, wait }
  }

  it('finds records that appear after its first check and give no watch event', async () => {
    const { records, watcher, wait } = silentWait(['entry-alpha.json', 'entry-beta.json'])
    writeFileSync(join(records, 'entry-alpha.json'), '{}')
    await vi.advanceTimersByTimeAsync(RECORD_CHECK_INTERVAL_MS)
    expect(watcher.close, 'one of the two records must not end the wait').not.toHaveBeenCalled()
    writeFileSync(join(records, 'entry-beta.json'), '{}')
    await vi.advanceTimersByTimeAsync(RECORD_CHECK_INTERVAL_MS)

    await expect(wait).resolves.toBeUndefined()
    expect(watcher.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  }, CANCELLATION_TEST_DEADLINE_MS)

  it('stops its interval check when the watcher fails', async () => {
    const { watcher, wait } = silentWait(['entry-alpha.json'])
    const failure = new Error('The controlled watcher fails.')
    watcher.emit('error', failure)

    await expect(wait).rejects.toBe(failure)
    expect(watcher.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops its interval check when the runner exits before the records appear', async () => {
    const { watcher, wait } = silentWait(['entry-alpha.json'], Promise.resolve({ code: 1, signal: null }))

    await expect(wait).rejects.toThrow('The private runner exited before it created entry-alpha.json.')
    expect(watcher.close).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('stopPrivateRunner', () => {
  it.each(['stop', 'completion'])('preserves the %s failure beside a fixture descendant cleanup failure', async (stage) => {
    const root = createFixture()
    const original = new Error(`The controlled runner ${stage} fails.`)
    const cleanup = new Error('The controlled fixture descendant cleanup fails.')
    const stop = vi.spyOn(processHelpers, 'stopProcesses')
    if (stage === 'stop')
      stop.mockRejectedValueOnce(original)
    else
      stop.mockResolvedValueOnce(undefined)
    vi.spyOn(processRegistry, 'stopTrackedProcesses').mockRejectedValueOnce(cleanup)
    const runner = {
      child: new ChildProcess(),
      completion: stage === 'completion' ? Promise.reject(original) : Promise.resolve({ code: 0, signal: null }),
      log: join(root, 'unused-console.log'),
    }
    const failure: unknown = await stopPrivateRunner(root, join(root, 'records'), runner).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The runner cleanup discarded one of its two failures.')
    expect(failure.errors).toEqual([original, cleanup])
    expect(processRegistry.stopTrackedProcesses).toHaveBeenCalledOnce()
  })
})

describe('runE2E Playwright integration', () => {
  it.each(CANCELLATION_CASES)('stops Task and its build command after $signal before Playwright starts', async ({ signal, code }) => {
    const root = createFixture({ holdBuild: true })
    const records = join(root, 'records')
    const runner = startPrivateRunner(root)
    await withCleanup(async () => {
      await waitForFixtureFiles(records, ['build-entry.json'], runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      const build = readFixtureRecord(join(records, 'build-entry.json'))
      console.info('Fixture build ownership:', JSON.stringify(build))
      cancelPrivateRunner(runner.child, signal)
      const result = await waitForRunnerCompletion(runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      expect(result).toEqual({ code, signal: null })
      requireProcessExit(build, 'processId')
      requireProcessExit(build, 'parentProcessId')
      expect(readdirSync(records).filter(file => file.startsWith('setup-'))).toEqual([])
      expect(existsSync(join(root, 'build-count'))).toBe(false)
      expect(existsSync(join(root, '.tmp'))).toBe(false)
      expect(readFileSync(runner.log, 'utf8')).toContain('fixture-build-entered')
    }, () => stopPrivateRunner(root, records, runner))
  }, CANCELLATION_TEST_DEADLINE_MS)

  it.each(CANCELLATION_CASES)('stops every fixture descendant and retains logs after $signal', async ({ signal, code }) => {
    const root = createFixture({ cancellation: true })
    const records = join(root, 'records')
    const runner = startPrivateRunner(root)
    await withCleanup(async () => {
      await waitForFixtureFiles(records, ['entry-alpha.json', 'entry-beta.json'], runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      cancelPrivateRunner(runner.child, signal)
      const result = await waitForRunnerCompletion(runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      expect(result).toEqual({ code, signal: null })
      expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('1')
      const setupFiles = readdirSync(records).filter(file => file.startsWith('setup-'))
      expect(setupFiles).toHaveLength(2)
      const states = setupFiles.map(file => readFixtureRecord(join(records, file)))
      expect(new Set(states.map(state => state.processId)).size).toBe(2)
      for (const state of states) {
        expect(existsSync(fixtureStringField(state, 'runDir'))).toBe(false)
        requireProcessExit(state, 'processId')
      }
      for (const label of ['alpha', 'beta']) {
        const entry = readFixtureRecord(join(records, `entry-${label}.json`))
        requireProcessExit(entry, 'workerProcessId')
        requireProcessExit(entry, 'ownedChildProcessId')
      }
      expect(readdirSync(join(root, '.tmp'))).toEqual([])
      const artifactRuns = join(root, 'retained-artifacts', 'runs')
      const runs = readdirSync(artifactRuns)
      expect(runs).toHaveLength(1)
      const outputFileDirectory = join(artifactRuns, runs[0]!)
      for (const index of [1, 2]) {
        const log = join(outputFileDirectory, `shard-${index}`, 'console.log')
        expect(readFileSync(log, 'utf8')).toContain('Running 1 test using 1 worker')
      }
      expect(readFileSync(runner.log, 'utf8')).toContain('Running 1 test using 1 worker')
    }, () => stopPrivateRunner(root, records, runner))
  }, CANCELLATION_TEST_DEADLINE_MS)

  it('selects only the failed parallel case when a serial run uses last-failed', async () => {
    const root = createFixture()
    const failed = await executeFixture(2, ['beta'], { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code).not.toBe(0)
    expect(failed.cases.map(test => test.status), launcherFixtureDiagnostics(failed)).toEqual(['expected', 'unexpected'])
    requireFixtureCleanupAndArtifacts(failed, 2)
    const precedingArtifacts = retainedArtifactFiles(root)
    expect([...precedingArtifacts.keys()].filter(path => path.endsWith('receipt.txt'))).toHaveLength(2)
    expect([...precedingArtifacts.keys()].some(path => path.endsWith('report.json'))).toBe(true)
    expect([...precedingArtifacts.keys()].some(path => path.endsWith('.zip'))).toBe(true)

    const resumed = await executeFixture(1, undefined, { root, args: ['--last-failed'], reportPath: join(root, 'second-report.json') })
    expect(resumed.code, launcherFixtureDiagnostics(resumed)).toBe(0)
    expect(resumed.cases.map(test => test.title)).toEqual(['executes beta'])
    expect(resumed.cases[0]?.results).toHaveLength(1)
    expect(resumed.cases[0]?.status).toBe('expected')
    expect(resumed.report.stats).toMatchObject({ expected: 1, unexpected: 0, skipped: 0, flaky: 0 })
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(true)
    expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('2')
    requireAllRunDirsRemoved(root)
    for (const [path, content] of precedingArtifacts) {
      expect(existsSync(path), `The serial run must keep the preceding evidence: ${path}`).toBe(true)
      expect(readFileSync(path)).toEqual(content)
    }
  }, INTEGRATION_DEADLINE_MS)

  it('clears prior parallel failures after a complete passing parallel run', async () => {
    const root = createFixture()
    const failed = await executeFixture(2, ['beta'], { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code).not.toBe(0)
    const beta = failed.cases.find(test => test.title === 'executes beta')
    expect(beta?.status).toBe('unexpected')
    expect(readFixtureRecord(join(root, 'retained-artifacts', '.last-run.json'))).toEqual({ status: 'failed', failedTests: [beta?.id] })

    const passed = await executeFixture(2, undefined, { root, reportPath: join(root, 'second-report.json') })
    expect(passed.code, launcherFixtureDiagnostics(passed)).toBe(0)
    expect(passed.cases.map(test => test.title)).toEqual(['executes alpha', 'executes beta'])
    expect(passed.report.stats).toMatchObject({ expected: 2, unexpected: 0, skipped: 0, flaky: 0 })
    expect(readFixtureRecord(join(root, 'retained-artifacts', '.last-run.json'))).toEqual({ status: 'passed', failedTests: [] })

    const empty = await executeFixture(1, undefined, { root, args: ['--last-failed', '--pass-with-no-tests'], reportPath: join(root, 'third-report.json') })
    expect(empty.code, launcherFixtureDiagnostics(empty)).toBe(0)
    expect(empty.cases).toEqual([])
    expect(empty.report.stats).toMatchObject({ expected: 0, unexpected: 0, skipped: 0, flaky: 0 })
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(false)
    expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('3')
    requireAllRunDirsRemoved(root)
  }, INTEGRATION_DEADLINE_MS)

  it('runs isolated Playwright shards in parallel and merges every case once', async () => {
    const run = await executeFixture(2)
    expect(run.code, launcherFixtureDiagnostics(run)).toBe(0)
    expect(run.parallelRelease).toBe(true)
    expect(run.report.stats).toMatchObject({ expected: 2, unexpected: 0, flaky: 0, skipped: 0 })
    expect(run.cases.map(test => test.status), launcherFixtureDiagnostics(run)).toEqual(['expected', 'expected'])
    requireFixtureCleanupAndArtifacts(run, 2)
    const entries = ['alpha', 'beta'].map(label => readFixtureRecord(join(run.records, `entry-${label}.json`)))
    expect(new Set(entries.map(entry => entry.workerProcessId)).size).toBe(2)
  }, INTEGRATION_DEADLINE_MS)

  it('runs one Playwright worker serially when the caller selects one worker', async () => {
    const run = await executeFixture(1)
    expect(run.code, launcherFixtureDiagnostics(run)).toBe(0)
    expect(run.parallelRelease).toBe(false)
    expect(run.report.stats).toMatchObject({ expected: 2, unexpected: 0, flaky: 0, skipped: 0 })
    requireFixtureCleanupAndArtifacts(run, 1)
    const alpha = readFixtureRecord(join(run.records, 'entry-alpha.json'))
    const beta = readFixtureRecord(join(run.records, 'entry-beta.json'))
    const alphaExit = readFixtureRecord(join(run.records, 'exit-alpha.json'))
    expect(alpha.workerProcessId).toBe(beta.workerProcessId)
    expect(BigInt(fixtureStringField(beta, 'started'))).toBeGreaterThanOrEqual(BigInt(fixtureStringField(alphaExit, 'finished')))
  }, INTEGRATION_DEADLINE_MS)

  it.each([
    { workers: 1 as const, parallelRelease: false },
    { workers: 2 as const, parallelRelease: true },
  ])('releases the cases of $workers worker(s) when the records watch reports no event', async ({ workers, parallelRelease }) => {
    const watcher = silenceNextWatch()
    const run = await executeFixture(workers)
    expect(run.code, launcherFixtureDiagnostics(run)).toBe(0)
    expect(run.cases.map(test => test.status), launcherFixtureDiagnostics(run)).toEqual(['expected', 'expected'])
    expect(run.parallelRelease).toBe(parallelRelease)
    expect(watcher.close).toHaveBeenCalledOnce()
  }, INTEGRATION_DEADLINE_MS)

  it('retains a failed fixture case and cleans both shards before returning failure', async () => {
    const run = await executeFixture(2, ['beta'])
    expect(run.code).not.toBe(0)
    expect(run.parallelRelease).toBe(true)
    expect(run.report.stats).toMatchObject({ expected: 1, unexpected: 1, flaky: 0, skipped: 0 })
    expect(run.cases.map(test => test.status), launcherFixtureDiagnostics(run)).toEqual(['expected', 'unexpected'])
    requireFixtureCleanupAndArtifacts(run, 2)
    const failed = run.cases.find(test => test.status === 'unexpected')
    expect(failed?.title).toBe('executes beta')
    expect(failed?.results[0]).toMatchObject({ status: 'failed', error: { message: expect.stringContaining('intentional fixture failure') } })
  }, INTEGRATION_DEADLINE_MS)

  it('balances parallel shards from the recorded duration history', async () => {
    const root = createFixture()
    const first = await executeFixture(2, undefined, { root, reportPath: join(root, 'first-report.json') })
    expect(first.code, launcherFixtureDiagnostics(first)).toBe(0)
    const history = readFixtureRecord(join(root, 'retained-artifacts', '.file-durations.json'))
    expect(history.version).toBe(1)
    if (!isObject(history.files))
      throw new Error('The duration history has no file map.')
    expect(Object.keys(history.files).sort()).toEqual(['alpha.spec.ts', 'beta.spec.ts'])

    const balanced = await executeFixture(2, undefined, { root, reportPath: join(root, 'second-report.json') })
    expect(balanced.code, launcherFixtureDiagnostics(balanced)).toBe(0)
    expect(balanced.cases.map(test => test.status)).toEqual(['expected', 'expected'])
    const log = readFileSync(balanced.consolePath, 'utf8')
    expect(log).toContain('E2E shard plan: 2 shards, balanced by the duration history')
    expect(log).toContain('    alpha.spec.ts:')
    expect(log).toContain('    beta.spec.ts:')

    const staticSplit = await executeFixture(2, undefined, { root, args: ['--balance=off'], reportPath: join(root, 'third-report.json') })
    expect(staticSplit.code, launcherFixtureDiagnostics(staticSplit)).toBe(0)
    expect(readFileSync(staticSplit.consolePath, 'utf8')).toContain('Playwright\'s own --shard=i/2 split. Reason: --balance=off selects it')
    expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('3')
    requireAllRunDirsRemoved(root)
  }, INTEGRATION_DEADLINE_MS)

  it('reruns the failed tests of the last parallel run in parallel shards', async () => {
    const root = createFixture()
    const failed = await executeFixture(2, ['beta'], { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code, launcherFixtureDiagnostics(failed)).not.toBe(0)

    const rerun = await runLauncherFixtureProject(root, { workers: 2, args: ['--last-failed'], expectedCases: ['beta'], reportPath: join(root, 'second-report.json') })
    expect(rerun.code, launcherFixtureDiagnostics(rerun)).toBe(0)
    expect(rerun.cases.map(test => test.title)).toEqual(['executes beta'])
    expect(rerun.report.stats).toMatchObject({ expected: 1, unexpected: 0, skipped: 0, flaky: 0 })
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(true)
    // The merged shards replace the caller's last-run state.
    expect(readFixtureRecord(join(root, 'retained-artifacts', '.last-run.json'))).toEqual({ status: 'passed', failedTests: [] })
    requireAllRunDirsRemoved(root)
  }, INTEGRATION_DEADLINE_MS)

  it('reruns the complete failed files of the last parallel run', async () => {
    const root = createFixture()
    const failed = await executeFixture(2, ['beta'], { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code, launcherFixtureDiagnostics(failed)).not.toBe(0)
    expect(existsSync(join(root, 'retained-artifacts', '.last-run-report.json'))).toBe(true)

    const rerun = await runLauncherFixtureProject(root, { workers: 2, args: ['--failed-files'], expectedCases: ['beta'], reportPath: join(root, 'second-report.json') })
    expect(rerun.code, launcherFixtureDiagnostics(rerun)).toBe(0)
    expect(rerun.cases.map(test => test.title)).toEqual(['executes beta'])
    expect(readFileSync(rerun.consolePath, 'utf8')).toContain('E2E --failed-files: 1 file from')
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(true)
    requireAllRunDirsRemoved(root)
  }, INTEGRATION_DEADLINE_MS)
})
