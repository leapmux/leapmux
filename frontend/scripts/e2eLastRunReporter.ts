import type { FullResult, Reporter, TestCase } from '@playwright/test/reporter'
import type { StateFileOperations } from './e2eStateFiles'
import { resolve } from 'node:path'
import process from 'node:process'
import { isObject } from '../src/lib/jsonPick'
import { absoluteDestination, readOptionalStateFile, writeFileAtomically } from './e2eStateFiles'

export interface NativeLastRunState {
  status: FullResult['status']
  failedTests: string[]
}

function lastRunPath(value: unknown): string {
  return absoluteDestination(value, 'native last-run destination')
}

/** Replace native last-run state only after its complete JSON reaches a private draft file. */
export function writeNativeLastRunState(path: string, state: NativeLastRunState, io?: StateFileOperations): void {
  writeFileAtomically(lastRunPath(path), JSON.stringify(state, null, 2), io)
}

/**
 * Resolve the native last-run state with the native precedence:
 * --last-failed-file, then a nonempty PLAYWRIGHT_LAST_RUN_OUTPUT_FILE, then the parent output root.
 * Native Playwright resolves a relative value against its own working directory, which is `cwd` here.
 * The native LastRunReporter reads its state from this path and writes its result to the same path.
 */
export function lastRunStatePath(lastFailedFile: string | undefined, env: NodeJS.ProcessEnv, cwd: string, parentLastRun: string): string {
  const explicit = lastFailedFile ?? (env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE || undefined)
  return explicit === undefined ? parentLastRun : resolve(cwd, explicit)
}

export interface LastFailedState {
  /** The native test IDs that the preceding run did not pass. */
  readonly failedTests: readonly string[]
  /** The exact file content. Every shard reads a copy of these bytes. */
  readonly content: string
}

/**
 * Read the state that --last-failed selects from.
 * Native Playwright ignores an absent or malformed state file and runs every selected test.
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

interface NativeTestSuite {
  allTests: () => readonly Pick<TestCase, 'id' | 'ok'>[]
}

/** Save the merged native IDs that Playwright uses for its next --last-failed selection. */
export default class E2ELastRunReporter implements Reporter {
  private suite: NativeTestSuite | undefined
  private readonly destination: string

  constructor() {
    this.destination = lastRunPath(process.env.PLAYWRIGHT_LAST_RUN_OUTPUT_FILE)
  }

  onBegin(_config: unknown, suite: NativeTestSuite): void {
    this.suite = suite
  }

  onEnd(result: Pick<FullResult, 'status'>): void {
    // Native outcomes settle after onBegin. Read each native predicate when the merged run ends.
    const failedTests = this.suite?.allTests().filter(test => !test.ok()).map(test => test.id) ?? []
    writeNativeLastRunState(this.destination, { status: result.status, failedTests })
  }

  printsToStdio(): boolean {
    return false
  }
}
