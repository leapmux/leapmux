import type { FullResult, Reporter, TestCase } from '@playwright/test/reporter'
import { randomUUID } from 'node:crypto'
import { mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, join } from 'node:path'
import process from 'node:process'

export interface NativeLastRunState {
  status: FullResult['status']
  failedTests: string[]
}

type LastRunFileOperations = Pick<typeof import('node:fs'), 'mkdirSync' | 'writeFileSync' | 'renameSync' | 'rmSync'>
const fileOperations: LastRunFileOperations = { mkdirSync, writeFileSync, renameSync, rmSync }

function lastRunPath(value: unknown): string {
  if (typeof value !== 'string' || !isAbsolute(value) || value.includes('\0'))
    throw new Error('The native last-run destination must be an absolute path without NUL characters.')
  return value
}

/** Replace native last-run state only after its complete JSON reaches a private draft file. */
export function writeNativeLastRunState(path: string, state: NativeLastRunState, io: LastRunFileOperations = fileOperations): void {
  const destination = lastRunPath(path)
  const directory = dirname(destination)
  io.mkdirSync(directory, { recursive: true })
  const draft = join(directory, `.leapmux-last-run-${randomUUID()}.writing`)
  try {
    io.writeFileSync(draft, JSON.stringify(state, null, 2), { encoding: 'utf8', flag: 'wx', mode: 0o600 })
    io.renameSync(draft, destination)
  }
  catch (error) {
    try {
      io.rmSync(draft, { force: true })
    }
    catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'The native last-run state write and draft cleanup failed.')
    }
    throw error
  }
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
