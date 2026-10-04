import { constants, copyFileSync, lstatSync, mkdirSync, readdirSync, readFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { isObject } from '../src/lib/jsonPick'

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

/** Visit native specifications while preserving their complete suite ancestry. */
function visitReportSpecs(report: unknown, visitSpec: (spec: NativeReportSpec) => void): void {
  if (!isObject(report) || !Array.isArray(report.suites) || !Array.isArray(report.errors) || report.errors.length !== 0)
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

/** Require exact selected case coverage before a complete run can succeed. */
export function assertMergedTestCoverage(path: string, expectedCases: readonly E2ECaseIdentity[]): void {
  const identities = (cases: readonly E2ECaseIdentity[]): string[] => cases.map(({ file, titlePath, line, column, projectId, projectName, repeatIndex }) =>
    JSON.stringify([file, titlePath, line, column, projectId, projectName, repeatIndex])).sort()
  const actual = identities(readDiscoveredTestCoverage(path).cases)
  if (JSON.stringify(actual) !== JSON.stringify(identities(expectedCases)))
    throw new Error('The merged E2E report does not contain every selected test case exactly once.')
}
