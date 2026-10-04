import type { Buffer } from 'node:buffer'
import type { FixturePolicy, FixtureRun, NativeCompletion, NativeRecord } from '../tests/e2e/helpers/nativeE2eFixture'
import { ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, watch, writeFileSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import process from 'node:process'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { isObject } from '../src/lib/jsonPick'
import { withCleanup } from '../tests/e2e/helpers/cleanup'
import { cleanupNativeE2eFixture, createNativeE2eFixture, executeNativeE2eFixture, nativeFixtureDiagnostics, nativeFixtureStringField, readNativeFixtureRecord, releaseNativeFixtureProcesses, startNativeE2eRunner } from '../tests/e2e/helpers/nativeE2eFixture'
import * as processHelpers from '../tests/e2e/helpers/process'
import * as processRegistry from '../tests/e2e/helpers/processRegistry'

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
      console.info('Native fixture failure evidence directory:', fixture)
    cleanupNativeE2eFixture(fixture, passed)
  }
  fixtures.clear()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

function createFixture(policy: FixturePolicy = {}): string {
  const root = createNativeE2eFixture(policy)
  fixtures.add(root)
  return root
}

interface RepeatedFixture {
  root: string
  args?: string[]
  reportPath: string
}

function executeFixture(workers: 1 | 2, failCase?: string, repeated?: RepeatedFixture): Promise<FixtureRun> {
  const root = repeated?.root ?? createFixture(failCase === undefined ? {} : { failCase })
  return executeNativeE2eFixture(root, {
    workers,
    ...(failCase === undefined ? {} : { failCase }),
    ...(repeated?.args === undefined ? {} : { args: repeated.args }),
    ...(repeated?.reportPath === undefined ? {} : { reportPath: repeated.reportPath }),
  })
}

function startPrivateRunner(root: string): ReturnType<typeof startNativeE2eRunner> {
  return startNativeE2eRunner(root)
}

function waitForNativeFiles(records: string, files: string[], completion: Promise<NativeCompletion>, signal: AbortSignal): Promise<void> {
  return new Promise((accept, reject) => {
    let finished = false
    const listener = watch(records, check)
    const finish = (error?: unknown) => {
      if (finished)
        return
      finished = true
      listener.close()
      signal.removeEventListener('abort', abort)
      if (error === undefined)
        accept()
      else
        reject(error)
    }
    function abort() {
      finish(new Error(`The native fixture did not create ${files.join(', ')} before the cancellation deadline.`))
    }
    function check() {
      if (files.every(file => existsSync(join(records, file))))
        finish()
    }
    listener.once('error', finish)
    signal.addEventListener('abort', abort, { once: true })
    void completion.then(() => finish(new Error(`The native runner exited before it created ${files.join(', ')}.`)), finish)
    if (signal.aborted)
      abort()
    else
      check()
  })
}

function waitForNativeCompletion(completion: Promise<NativeCompletion>, signal: AbortSignal): Promise<NativeCompletion> {
  return new Promise((accept, reject) => {
    const abort = () => reject(new Error('The native runner did not exit before the cancellation deadline.'))
    signal.addEventListener('abort', abort, { once: true })
    void completion.then(accept, reject).finally(() => signal.removeEventListener('abort', abort))
    if (signal.aborted)
      abort()
  })
}

function requireProcessExit(record: NativeRecord, key: string): void {
  const pid = record[key]
  if (typeof pid !== 'number' || !Number.isSafeInteger(pid) || pid <= 0)
    throw new Error(`The native fixture record has no valid ${key}.`)
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
  releaseNativeFixtureProcesses(records)
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
      fields = ['workerProcessId', 'nativeChildProcessId']
    else if (/^owned-child-(?:alpha|beta)\.json$/u.test(file))
      fields = ['nativeChildProcessId']
    else if (file === 'build-entry.json')
      fields = ['processId', 'parentProcessId']
    if (fields.length === 0)
      continue
    const record = readNativeFixtureRecord(join(records, file))
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

function requireNativeCleanupAndArtifacts(run: FixtureRun, setupCount: number): void {
  expect(readFileSync(join(run.root, 'build-count'), 'utf8')).toBe('1')
  const setupFiles = readdirSync(run.records).filter(file => file.startsWith('setup-'))
  const teardownFiles = readdirSync(run.records).filter(file => file.startsWith('teardown-'))
  expect(setupFiles).toHaveLength(setupCount)
  expect(teardownFiles).toHaveLength(setupCount)
  const states = setupFiles.map(file => readNativeFixtureRecord(join(run.records, file)))
  const teardowns = teardownFiles.map(file => readNativeFixtureRecord(join(run.records, file)))
  expect(new Set(states.map(state => nativeFixtureStringField(state, 'nativeRoot'))).size).toBe(setupCount)
  expect(new Set(states.map(state => nativeFixtureStringField(state, 'nonce'))).size).toBe(setupCount)
  expect(new Set(states.map(state => nativeFixtureStringField(state, 'noncePath'))).size).toBe(setupCount)
  expect(new Set(states.map(state => nativeFixtureStringField(state, 'statePath'))).size).toBe(setupCount)
  expect(new Set(states.map(state => state.processId)).size).toBe(setupCount)
  expect(new Set(states.map(state => state.port)).size).toBe(setupCount)
  for (const state of states) {
    expect(state.workers).toBe(1)
    const nativeRoot = nativeFixtureStringField(state, 'nativeRoot')
    expect(state.nativeRootIsSymlink).toBe(false)
    const runRoot = setupCount === 1 ? nativeRoot : dirname(nativeRoot)
    expect(basename(runRoot)).toMatch(/^e-[A-Za-z0-9]{6}$/)
    if (setupCount > 1)
      expect(basename(nativeRoot)).toMatch(/^[1-9]\d*$/)
    const inside = relative(join(run.root, '.tmp'), nativeRoot)
    expect(isAbsolute(inside)).toBe(false)
    expect(inside === '..' || inside.startsWith(`..${sep}`)).toBe(false)
    expect(dirname(nativeFixtureStringField(state, 'binaryPath'))).toBe(nativeRoot)
    expect(dirname(nativeFixtureStringField(state, 'noncePath'))).toBe(nativeRoot)
    expect(existsSync(nativeRoot)).toBe(false)
    expect(existsSync(nativeFixtureStringField(state, 'binaryPath'))).toBe(false)
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
      throw new Error('The native fixture result has no attachment array.')
    const attachments = result.attachments.filter(attachment => isObject(attachment) && attachment.name === 'isolation-receipt')
    expect(attachments).toHaveLength(1)
    const attachment = attachments[0]
    if (!isObject(attachment))
      throw new Error('The native fixture result has no isolation receipt.')
    const path = nativeFixtureStringField(attachment, 'path')
    const retainedPath = isAbsolute(path) ? path : resolve(run.root, 'frontend', path)
    expect(readFileSync(retainedPath, 'utf8')).toBe(`native-case-${test.title.slice('executes '.length)}`)
  }
  for (const label of ['alpha', 'beta']) {
    const entry = readNativeFixtureRecord(join(run.records, `entry-${label}.json`))
    expect(entry.parallelIndex).toBe(0)
    expect(states.map(state => state.nonce)).toContain(entry.nonce)
    expect(states.map(state => state.port)).toContain(entry.port)
  }
}

function requireAllNativeRootsRemoved(root: string): void {
  const records = join(root, 'records')
  for (const file of readdirSync(records).filter(file => file.startsWith('setup-') && file.endsWith('.json'))) {
    const state = readNativeFixtureRecord(join(records, file))
    expect(existsSync(nativeFixtureStringField(state, 'nativeRoot'))).toBe(false)
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
        throw new Error('The retained native evidence contains an unexpected filesystem entry.')
    }
  }
  visit(join(root, 'retained-artifacts', 'runs'))
  return files
}

describe('stopPrivateRunner', () => {
  it.each(['stop', 'completion'])('preserves the %s failure beside a native descendant cleanup failure', async (stage) => {
    const root = createFixture()
    const original = new Error(`The controlled runner ${stage} fails.`)
    const cleanup = new Error('The controlled native descendant cleanup fails.')
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

describe('runE2E native integration', () => {
  it.each(CANCELLATION_CASES)('stops Task and its build command after $signal before Playwright starts', async ({ signal, code }) => {
    const root = createFixture({ holdBuild: true })
    const records = join(root, 'records')
    const runner = startPrivateRunner(root)
    await withCleanup(async () => {
      await waitForNativeFiles(records, ['build-entry.json'], runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      const build = readNativeFixtureRecord(join(records, 'build-entry.json'))
      console.info('Native build ownership:', JSON.stringify(build))
      cancelPrivateRunner(runner.child, signal)
      const result = await waitForNativeCompletion(runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      expect(result).toEqual({ code, signal: null })
      requireProcessExit(build, 'processId')
      requireProcessExit(build, 'parentProcessId')
      expect(readdirSync(records).filter(file => file.startsWith('setup-'))).toEqual([])
      expect(existsSync(join(root, 'build-count'))).toBe(false)
      expect(existsSync(join(root, '.tmp'))).toBe(false)
      expect(readFileSync(runner.log, 'utf8')).toContain('native-build-entered')
    }, () => stopPrivateRunner(root, records, runner))
  }, CANCELLATION_TEST_DEADLINE_MS)

  it.each(CANCELLATION_CASES)('stops every native descendant and retains logs after $signal', async ({ signal, code }) => {
    const root = createFixture({ cancellation: true })
    const records = join(root, 'records')
    const runner = startPrivateRunner(root)
    await withCleanup(async () => {
      await waitForNativeFiles(records, ['entry-alpha.json', 'entry-beta.json'], runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      cancelPrivateRunner(runner.child, signal)
      const result = await waitForNativeCompletion(runner.completion, AbortSignal.timeout(CANCELLATION_PHASE_DEADLINE_MS))
      expect(result).toEqual({ code, signal: null })
      expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('1')
      const setupFiles = readdirSync(records).filter(file => file.startsWith('setup-'))
      expect(setupFiles).toHaveLength(2)
      const states = setupFiles.map(file => readNativeFixtureRecord(join(records, file)))
      expect(new Set(states.map(state => state.processId)).size).toBe(2)
      for (const state of states) {
        expect(existsSync(nativeFixtureStringField(state, 'nativeRoot'))).toBe(false)
        requireProcessExit(state, 'processId')
      }
      for (const label of ['alpha', 'beta']) {
        const entry = readNativeFixtureRecord(join(records, `entry-${label}.json`))
        requireProcessExit(entry, 'workerProcessId')
        requireProcessExit(entry, 'nativeChildProcessId')
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
    const failed = await executeFixture(2, 'beta', { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code).not.toBe(0)
    expect(failed.cases.map(test => test.status), nativeFixtureDiagnostics(failed)).toEqual(['expected', 'unexpected'])
    requireNativeCleanupAndArtifacts(failed, 2)
    const precedingArtifacts = retainedArtifactFiles(root)
    expect([...precedingArtifacts.keys()].filter(path => path.endsWith('receipt.txt'))).toHaveLength(2)
    expect([...precedingArtifacts.keys()].some(path => path.endsWith('report.json'))).toBe(true)
    expect([...precedingArtifacts.keys()].some(path => path.endsWith('.zip'))).toBe(true)

    const resumed = await executeFixture(1, undefined, { root, args: ['--last-failed'], reportPath: join(root, 'second-report.json') })
    expect(resumed.code, nativeFixtureDiagnostics(resumed)).toBe(0)
    expect(resumed.cases.map(test => test.title)).toEqual(['executes beta'])
    expect(resumed.cases[0]?.results).toHaveLength(1)
    expect(resumed.cases[0]?.status).toBe('expected')
    expect(resumed.report.stats).toMatchObject({ expected: 1, unexpected: 0, skipped: 0, flaky: 0 })
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(true)
    expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('2')
    requireAllNativeRootsRemoved(root)
    for (const [path, content] of precedingArtifacts) {
      expect(existsSync(path), `The serial run must preserve preceding native evidence: ${path}`).toBe(true)
      expect(readFileSync(path)).toEqual(content)
    }
  }, INTEGRATION_DEADLINE_MS)

  it('clears prior parallel failures after a complete passing parallel run', async () => {
    const root = createFixture()
    const failed = await executeFixture(2, 'beta', { root, reportPath: join(root, 'first-report.json') })
    expect(failed.code).not.toBe(0)
    const beta = failed.cases.find(test => test.title === 'executes beta')
    expect(beta?.status).toBe('unexpected')
    expect(readNativeFixtureRecord(join(root, 'retained-artifacts', '.last-run.json'))).toEqual({ status: 'failed', failedTests: [beta?.id] })

    const passed = await executeFixture(2, undefined, { root, reportPath: join(root, 'second-report.json') })
    expect(passed.code, nativeFixtureDiagnostics(passed)).toBe(0)
    expect(passed.cases.map(test => test.title)).toEqual(['executes alpha', 'executes beta'])
    expect(passed.report.stats).toMatchObject({ expected: 2, unexpected: 0, skipped: 0, flaky: 0 })
    expect(readNativeFixtureRecord(join(root, 'retained-artifacts', '.last-run.json'))).toEqual({ status: 'passed', failedTests: [] })

    const empty = await executeFixture(1, undefined, { root, args: ['--last-failed', '--pass-with-no-tests'], reportPath: join(root, 'third-report.json') })
    expect(empty.code, nativeFixtureDiagnostics(empty)).toBe(0)
    expect(empty.cases).toEqual([])
    expect(empty.report.stats).toMatchObject({ expected: 0, unexpected: 0, skipped: 0, flaky: 0 })
    expect(existsSync(join(root, 'records', 'entry-alpha.json'))).toBe(false)
    expect(existsSync(join(root, 'records', 'entry-beta.json'))).toBe(false)
    expect(readFileSync(join(root, 'build-count'), 'utf8')).toBe('3')
    requireAllNativeRootsRemoved(root)
  }, INTEGRATION_DEADLINE_MS)

  it('runs isolated native shards in parallel and merges every case once', async () => {
    const run = await executeFixture(2)
    expect(run.code, nativeFixtureDiagnostics(run)).toBe(0)
    expect(run.parallelRelease).toBe(true)
    expect(run.report.stats).toMatchObject({ expected: 2, unexpected: 0, flaky: 0, skipped: 0 })
    expect(run.cases.map(test => test.status), nativeFixtureDiagnostics(run)).toEqual(['expected', 'expected'])
    requireNativeCleanupAndArtifacts(run, 2)
    const entries = ['alpha', 'beta'].map(label => readNativeFixtureRecord(join(run.records, `entry-${label}.json`)))
    expect(new Set(entries.map(entry => entry.workerProcessId)).size).toBe(2)
  }, INTEGRATION_DEADLINE_MS)

  it('runs one native worker serially when the caller selects one worker', async () => {
    const run = await executeFixture(1)
    expect(run.code, nativeFixtureDiagnostics(run)).toBe(0)
    expect(run.parallelRelease).toBe(false)
    expect(run.report.stats).toMatchObject({ expected: 2, unexpected: 0, flaky: 0, skipped: 0 })
    requireNativeCleanupAndArtifacts(run, 1)
    const alpha = readNativeFixtureRecord(join(run.records, 'entry-alpha.json'))
    const beta = readNativeFixtureRecord(join(run.records, 'entry-beta.json'))
    const alphaExit = readNativeFixtureRecord(join(run.records, 'exit-alpha.json'))
    expect(alpha.workerProcessId).toBe(beta.workerProcessId)
    expect(BigInt(nativeFixtureStringField(beta, 'started'))).toBeGreaterThanOrEqual(BigInt(nativeFixtureStringField(alphaExit, 'finished')))
  }, INTEGRATION_DEADLINE_MS)

  it('retains a failed native case and cleans both shards before returning failure', async () => {
    const run = await executeFixture(2, 'beta')
    expect(run.code).not.toBe(0)
    expect(run.parallelRelease).toBe(true)
    expect(run.report.stats).toMatchObject({ expected: 1, unexpected: 1, flaky: 0, skipped: 0 })
    expect(run.cases.map(test => test.status), nativeFixtureDiagnostics(run)).toEqual(['expected', 'unexpected'])
    requireNativeCleanupAndArtifacts(run, 2)
    const failed = run.cases.find(test => test.status === 'unexpected')
    expect(failed?.title).toBe('executes beta')
    expect(failed?.results[0]).toMatchObject({ status: 'failed', error: { message: expect.stringContaining('intentional fixture failure') } })
  }, INTEGRATION_DEADLINE_MS)
})
