import { constants, copyFileSync, lstatSync, mkdirSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { isObject } from '../src/lib/jsonPick'
import { readOptionalStateFile } from './e2eStateFiles'

/**
 * The output root keeps a copy of the combined JSON report of the last parallel run under this name.
 * A serial run writes no such report, so a serial run makes the copy older than the last-run state.
 */
export const LAST_RUN_REPORT_FILE = '.last-run-report.json'

const REPORTER_DESTINATIONS = [
  'PLAYWRIGHT_BLOB_OUTPUT_FILE',
  'PLAYWRIGHT_BLOB_OUTPUT_DIR',
  'PLAYWRIGHT_BLOB_OUTPUT_NAME',
  'PLAYWRIGHT_JSON_OUTPUT_FILE',
  'PLAYWRIGHT_JSON_OUTPUT_DIR',
  'PLAYWRIGHT_JSON_OUTPUT_NAME',
  'PLAYWRIGHT_HTML_OUTPUT_DIR',
  'PLAYWRIGHT_HTML_REPORT',
  'PLAYWRIGHT_JUNIT_OUTPUT_FILE',
  'PLAYWRIGHT_JUNIT_OUTPUT_DIR',
  'PLAYWRIGHT_JUNIT_OUTPUT_NAME',
  'PLAYWRIGHT_LAST_RUN_OUTPUT_FILE',
]

/** Keep every reporter destination inside the owning shard. Do not mutate the parent environment. */
export function shardReporterEnvironment(env: NodeJS.ProcessEnv, outputFileDir: string): NodeJS.ProcessEnv {
  const result = { ...env }
  for (const key of REPORTER_DESTINATIONS)
    delete result[key]
  return {
    ...result,
    LEAPMUX_E2E_OUTPUT_FILE_DIR: outputFileDir,
    PLAYWRIGHT_BLOB_OUTPUT_DIR: join(outputFileDir, 'blob-report'),
    PLAYWRIGHT_JSON_OUTPUT_FILE: join(outputFileDir, 'report.json'),
    PLAYWRIGHT_HTML_OUTPUT_DIR: join(outputFileDir, 'html-report'),
    PLAYWRIGHT_HTML_OPEN: 'never',
    PLAYWRIGHT_JUNIT_OUTPUT_FILE: join(outputFileDir, 'junit.xml'),
    PLAYWRIGHT_LAST_RUN_OUTPUT_FILE: join(outputFileDir, 'test-results', '.last-run.json'),
  }
}

/** Resolve the caller's JSON destination with Playwright's environment precedence. */
export function mergedJsonDestination(env: NodeJS.ProcessEnv, cwd: string, outputFileDir: string): string {
  if (env.PLAYWRIGHT_JSON_OUTPUT_FILE)
    return resolve(cwd, env.PLAYWRIGHT_JSON_OUTPUT_FILE)
  if (env.PLAYWRIGHT_JSON_OUTPUT_NAME)
    return resolve(cwd, env.PLAYWRIGHT_JSON_OUTPUT_DIR ?? '', env.PLAYWRIGHT_JSON_OUTPUT_NAME)
  return join(outputFileDir, 'report.json')
}

interface NativeReportSpec {
  file: string
  tests: unknown[]
  source: Record<string, unknown>
  ancestors: Record<string, unknown>[]
}

/**
 * Visit native specifications while preserving their complete suite ancestry.
 * Coverage requires a report without a global error. A failed or stopped run still reports each test outcome.
 */
function visitReportSpecs(report: unknown, visitSpec: (spec: NativeReportSpec) => void, globalErrors: 'reject' | 'accept' = 'reject'): void {
  if (!isObject(report) || !Array.isArray(report.suites) || !Array.isArray(report.errors) || (globalErrors === 'reject' && report.errors.length !== 0))
    throw new Error('The Playwright report is absent, malformed, or contains a global error.')
  const visit = (suite: unknown, ancestors: Record<string, unknown>[]): void => {
    if (!isObject(suite) || !Array.isArray(suite.specs) || (suite.suites !== undefined && !Array.isArray(suite.suites)))
      throw new Error('The Playwright report contains an incomplete suite.')
    const path = [...ancestors, suite]
    for (const spec of suite.specs) {
      if (!isObject(spec) || typeof spec.file !== 'string' || !spec.file || !Array.isArray(spec.tests) || spec.tests.length === 0)
        throw new Error('The Playwright report contains an incomplete test.')
      visitSpec({ file: spec.file, tests: spec.tests, source: spec, ancestors: path })
    }
    for (const child of suite.suites ?? [])
      visit(child, path)
  }
  for (const suite of report.suites)
    visit(suite, [])
}

export interface E2ECaseIdentity {
  readonly file: string
  readonly titlePath: readonly string[]
  readonly line: number
  readonly column: number
  readonly projectId: string
  readonly projectName: string
  readonly repeatIndex: number
}

export interface E2ETestCoverage {
  files: string[]
  cases: E2ECaseIdentity[]
}

function stringIdentity(value: unknown, field: string): string {
  if (typeof value !== 'string')
    throw new Error(`The Playwright report contains an invalid ${field} identity.`)
  return value
}

function sourcePosition(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)
    throw new Error(`The Playwright report contains an invalid source ${field}.`)
  return value
}

/** Read each selected specification, project, and repeat from the native JSON report. */
export function discoveredTestCoverage(report: unknown): E2ETestCoverage {
  const files = new Set<string>()
  const cases: E2ECaseIdentity[] = []
  const repeats = new Map<string, number>()
  visitReportSpecs(report, (spec) => {
    files.add(spec.file)
    const titlePath = [
      ...spec.ancestors.map(suite => stringIdentity(suite.title, 'suite title')),
      stringIdentity(spec.source.title, 'test title'),
    ]
    const line = sourcePosition(spec.source.line, 'line')
    const column = sourcePosition(spec.source.column, 'column')
    for (const test of spec.tests) {
      if (!isObject(test))
        throw new Error('The Playwright report contains an incomplete test identity.')
      const projectName = stringIdentity(test.projectName, 'project name')
      // Native blob merge drops __projectId while preserving the project name.
      // Preserve a present ID. Exact coverage rejects ambiguous merged project names.
      const projectId = test.projectId === undefined ? projectName : stringIdentity(test.projectId, 'project ID')
      // Native JSON omits repeatEachIndex and merges repeats into this array.
      // Count each specification and project separately to retain every repeat.
      const identity = JSON.stringify([spec.file, titlePath, line, column, projectId, projectName])
      const repeatIndex = repeats.get(identity) ?? 0
      repeats.set(identity, repeatIndex + 1)
      cases.push({ file: spec.file, titlePath, line, column, projectId, projectName, repeatIndex })
    }
  })
  return { files: [...files].sort(), cases }
}

export function readDiscoveredTestCoverage(path: string): E2ETestCoverage {
  return discoveredTestCoverage(JSON.parse(readFileSync(path, 'utf8')))
}

/** Collect one complete native blob from each finished shard without replacing another shard's file. */
export function collectShardBlobs(artifactDirs: readonly string[], destination: string): void {
  mkdirSync(destination, { recursive: true })
  for (const [index, outputFileDir] of artifactDirs.entries()) {
    const directory = join(outputFileDir, 'blob-report')
    const files = readdirSync(directory, { withFileTypes: true }).filter(entry => entry.name.endsWith('.zip'))
    if (files.length !== 1 || !files[0]!.isFile())
      throw new Error(`E2E shard ${index + 1} produced no unique native blob report.`)
    const file = join(directory, files[0]!.name)
    const stat = lstatSync(file)
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size === 0)
      throw new Error(`E2E shard ${index + 1} produced an incomplete native blob report.`)
    copyFileSync(file, join(destination, `shard-${index + 1}-${basename(file)}`), constants.COPYFILE_EXCL)
  }
}

/** Require exact selected case coverage in the parsed merged report before a complete run can succeed. */
export function assertMergedTestCoverage(report: unknown, expectedCases: readonly E2ECaseIdentity[]): void {
  const identities = (cases: readonly E2ECaseIdentity[]): string[] => cases.map(({ file, titlePath, line, column, projectId, projectName, repeatIndex }) =>
    JSON.stringify([file, titlePath, line, column, projectId, projectName, repeatIndex])).sort()
  const actual = identities(discoveredTestCoverage(report).cases)
  if (JSON.stringify(actual) !== JSON.stringify(identities(expectedCases)))
    throw new Error('The merged E2E report does not contain every selected test case exactly once.')
}

export interface FileDuration {
  /** The sum of the measured result durations of the file's cases, in milliseconds. */
  readonly durationMs: number
  /** The number of cases that the sum includes. */
  readonly cases: number
}

/**
 * Sum the native result durations of each file.
 * A case without a result, or with an unfinished result (native duration -1), adds nothing.
 */
export function reportedFileDurations(report: unknown): Map<string, FileDuration> {
  const totals = new Map<string, FileDuration>()
  visitReportSpecs(report, (spec) => {
    for (const test of spec.tests) {
      if (!isObject(test) || !Array.isArray(test.results))
        throw new Error('The Playwright report contains a test without a result array.')
      let durationMs = 0
      let finished = test.results.length > 0
      for (const result of test.results) {
        if (!isObject(result) || typeof result.duration !== 'number' || !Number.isFinite(result.duration))
          throw new Error('The Playwright report contains an invalid result duration.')
        if (result.duration < 0)
          finished = false
        durationMs += result.duration
      }
      if (!finished)
        continue
      const total = totals.get(spec.file) ?? { durationMs: 0, cases: 0 }
      totals.set(spec.file, { durationMs: total.durationMs + durationMs, cases: total.cases + 1 })
    }
  }, 'accept')
  return totals
}

const TEST_OUTCOMES = new Set(['expected', 'unexpected', 'flaky', 'skipped'])
const TEST_STATUSES = new Set(['passed', 'failed', 'timedOut', 'skipped', 'interrupted'])

/**
 * Decide whether one native test leaves its file without a complete clean result.
 * - `unexpected` covers a failure and a timeout. A `test.fail()` case that fails is `expected`.
 * - `flaky` passed only on a retry. A complete-file result requires zero retries.
 * - A `skipped` outcome for a test that does not skip on purpose shows an interruption, a run deadline,
 *   a failure limit, or a crash that left the test without a result.
 */
function testNeedsRerun(test: unknown): boolean {
  if (!isObject(test) || typeof test.status !== 'string' || !TEST_OUTCOMES.has(test.status)
    || typeof test.expectedStatus !== 'string' || !TEST_STATUSES.has(test.expectedStatus) || !Array.isArray(test.results)) {
    throw new Error('The Playwright report contains an incomplete test outcome.')
  }
  if (!test.results.every(result => isObject(result) && typeof result.status === 'string' && TEST_STATUSES.has(result.status)))
    throw new Error('The Playwright report contains an incomplete test result.')
  if (test.status === 'unexpected' || test.status === 'flaky')
    return true
  if (test.status === 'skipped' && test.expectedStatus !== 'skipped')
    return true
  return test.results.some(result => isObject(result) && result.status === 'interrupted')
}

/** List each file that holds at least one test without a complete clean result. */
export function failedReportFiles(report: unknown): string[] {
  const files = new Set<string>()
  visitReportSpecs(report, (spec) => {
    // Validate every test, so a malformed report fails even when an earlier test already selects the file.
    const rerun = spec.tests.map(testNeedsRerun)
    if (rerun.includes(true))
      files.add(spec.file)
  }, 'accept')
  return [...files].sort()
}

/** Read the files that --failed-files reruns. Refuse an absent, unreadable, or malformed report. */
export function readFailedReportFiles(path: string): string[] {
  let content: string | undefined
  try {
    content = readOptionalStateFile(path)
  }
  catch (error) {
    throw new Error(`The --failed-files option cannot read the combined report at ${path}.`, { cause: error })
  }
  if (content === undefined)
    throw new Error(`The --failed-files option reads the combined report at ${path}, but that file does not exist. Run the E2E tests once in parallel, or give an existing report with --failed-files-from=<report.json>.`)
  try {
    return failedReportFiles(JSON.parse(content))
  }
  catch (error) {
    throw new Error(`The combined report at ${path} is not a valid Playwright JSON report.`, { cause: error })
  }
}

/** Read the modification time of a file. Return undefined only when the file does not exist, and throw every other error. */
function modifiedAt(path: string): number | undefined {
  try {
    return statSync(path).mtimeMs
  }
  catch (error) {
    if (isObject(error) && error.code === 'ENOENT')
      return undefined
    throw error
  }
}

/**
 * Refuse a report that an earlier run wrote.
 * A parallel run saves the report right after its merge replaced the last-run state, so the report is not older than that state.
 * A serial run replaces the state and saves no report. A report older than the state therefore describes an earlier run.
 * An absent report is left to its reader, and an absent state shows no later run.
 */
export function assertReportIsCurrent(report: string, state: string): void {
  const reportTime = modifiedAt(report)
  const stateTime = modifiedAt(state)
  if (reportTime === undefined || stateTime === undefined || reportTime >= stateTime)
    return
  throw new Error(`The combined report at ${report} is older than the last-run state at ${state}. A later run replaced the state, and only a parallel run saves the report. Run the E2E tests once in parallel, or give an existing report with --failed-files-from=<report.json>.`)
}
