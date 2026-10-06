import type { FullResult, Reporter, TestCase } from '@playwright/test/reporter'
import type { StateFileOperations } from './e2eStateFiles'
import { resolve } from 'node:path'
import process from 'node:process'
import { isObject } from '../src/lib/jsonPick'
import { absoluteDestination, readOptionalStateFile, writeFileAtomically } from './e2eStateFiles'

/** The last-run state in the format that Playwright reads for --last-failed. */
export interface PlaywrightLastRunState {
  status: FullResult['status']
  failedTests: string[]
}

function lastRunPath(value: unknown): string {
  return absoluteDestination(value, 'Playwright last-run destination')
}

/** Replace the Playwright last-run state only after its complete JSON reaches a private draft file. */
export function writePlaywrightLastRunState(path: string, state: PlaywrightLastRunState, io?: StateFileOperations): void {
  writeFileAtomically(lastRunPath(path), JSON.stringify(state, null, 2), io)
}

/**
 * Resolve the Playwright last-run state with Playwright's precedence:
 * --last-failed-file, then a nonempty PLAYWRIGHT_LAST_RUN_OUTPUT_FILE, then the parent output root.
 * Playwright resolves a relative value against its own working directory, which is `cwd` here.
 * Playwright's own LastRunReporter reads its state from this path and writes its result to the same path.
 */
export function lastRunStatePath(lastFailedFile: string | undefined, env: NodeJS.ProcessEnv, cwd: string, parentLastRun: string): string {
  const explicit = lastFailedFile ?? (env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE || undefined)
  return explicit === undefined ? parentLastRun : resolve(cwd, explicit)
}

export interface LastFailedState {
  /** The Playwright test IDs that the preceding run did not pass. */
  readonly failedTests: readonly string[]
  /** The exact file content. Every shard reads a copy of these bytes. */
  readonly content: string
}

/**
 * Read the state that --last-failed selects from.
 * Playwright ignores an absent or malformed state file and runs every selected test.
 * This launcher refuses that state, because a rerun of the failures must never become a complete run.
 */
export function readLastFailedState(path: string): LastFailedState {
  let content: string | undefined
  try {
    content = readOptionalStateFile(path)
  }
  catch (error) {
    throw new Error(`The --last-failed option cannot read the last-run state at ${path}.`, { cause: error })
  }
  if (content === undefined)
    throw new Error(`The --last-failed option reads the last-run state at ${path}, but that file does not exist. Run the E2E tests once without --last-failed. If that run used --output=<directory>, give the same option again.`)
  let value: unknown
  try {
    value = JSON.parse(content)
  }
  catch (error) {
    throw new Error(`The last-run state at ${path} is not valid JSON. Run the E2E tests without --last-failed to replace it.`, { cause: error })
  }
  if (!isObject(value) || !Array.isArray(value.failedTests) || !value.failedTests.every(id => typeof id === 'string' && id !== ''))
    throw new Error(`The last-run state at ${path} has no failedTests array of test IDs. Run the E2E tests without --last-failed to replace it.`)
  return { failedTests: value.failedTests, content }
}

/** The part of the Playwright suite that this reporter reads. */
interface PlaywrightSuite {
  allTests: () => readonly Pick<TestCase, 'id' | 'ok'>[]
}

/** Save the merged test IDs that Playwright uses for its next --last-failed selection. */
export default class E2ELastRunReporter implements Reporter {
  private suite: PlaywrightSuite | undefined
  private readonly destination: string

  constructor() {
    this.destination = lastRunPath(process.env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE)
  }

  onBegin(_config: unknown, suite: PlaywrightSuite): void {
    this.suite = suite
  }

  onEnd(result: Pick<FullResult, 'status'>): void {
    // Playwright settles each test outcome after onBegin. Read each outcome predicate when the merged run ends.
    const failedTests = this.suite?.allTests().filter(test => !test.ok()).map(test => test.id) ?? []
    writePlaywrightLastRunState(this.destination, { status: result.status, failedTests })
  }

  printsToStdio(): boolean {
    return false
  }
}
