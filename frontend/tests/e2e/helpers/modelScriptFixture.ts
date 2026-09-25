import type { MockModelRule, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import { randomUUID } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import {
  extendMockModelScenario,
  mockScenarioPrompt,
  readScenarioStatus,
  registerMockModelScenario,
  removeMockModelScenario,
} from './mockModelScenario'
import { getGlobalState } from './server'

/** Long enough for an agent process to start and run a turn on a loaded machine. */
const STEP_WAIT_TIMEOUT_MS = 180_000

/**
 * How long before the test's own deadline a stalled step wait gives up.
 *
 * The wait's error states how far the script got and which requests it did not
 * answer. Playwright's timeout states only that the test ran out of time, so the
 * wait must end first, with time left for the error and the fixture's teardown.
 */
export const STEP_WAIT_REPORT_MARGIN_MS = 5_000

/** Options of `startModelScript`. */
export interface ModelScriptOptions {
  /**
   * The time (epoch milliseconds) at which the test's own timeout ends it, or
   * undefined for a test with no timeout. It is read at each wait, so a test that
   * raised its timeout before the wait keeps the longer one.
   */
  testDeadline?: () => number | undefined
}

/**
 * One model script for the lifetime of one test.
 *
 * A test queues the answers it expects, marks each prompt it sends, and the
 * fixture verifies at teardown that the agent consumed exactly that script. A
 * turn the test did not script therefore fails the test that caused it, rather
 * than the next one to run against the shared process.
 */
export interface ModelScript {
  /** The scenario identifier, which also appears in a failure attachment. */
  id: string
  /** Mark a prompt so the requests it causes reach this test's script. */
  prompt: (text: string) => string
  /** Append answers to the ordered queue, in the order the agent will ask. */
  queue: (...steps: MockModelStep[]) => Promise<void>
  /**
   * Add a rule that answers every matching request without consuming a step.
   *
   * Use this for a turn whose count the test cannot predict: a retry, a
   * provider's own summary, a subagent that runs an unknown number of turns.
   */
  rule: (...rules: MockModelRule[]) => Promise<void>
  /**
   * Answer every request that outlives the queue with one step.
   *
   * Without this, an unscripted turn fails the test, which is the default and
   * the point. Use it only where the turn count is genuinely unknowable: an
   * approval that restarts the agent, a provider that retries on its own.
   * State the reason at the call site.
   */
  fallback: (step: MockModelStep) => Promise<void>
  /** The live consumption state. */
  status: () => Promise<MockModelScenarioStatus>
  /**
   * Wait until the agent consumed the answers this script queued.
   *
   * `count` defaults to every answer queued so far, which is what a test wants
   * after each send, however many turns it has run.
   *
   * This is the authoritative signal that a turn ran: the mock endpoint counts
   * a step when the agent asks for it. The thinking indicator is not — it can
   * still be absent while the agent process finishes starting, so a wait on it
   * returns before the turn begins and the test then reads an empty transcript.
   */
  waitForSteps: (count?: number, timeoutMs?: number) => Promise<MockModelScenarioStatus>
  /**
   * Accept an unconsumed queue at teardown, for the stated reason.
   *
   * A test needs this only when it ends a turn before the agent asks for every
   * answer — an interrupt, or a worker restart mid-turn.
   */
  allowUnconsumed: (reason: string) => void
}

export interface ModelScriptLifecycle {
  script: ModelScript
  /** Verify consumption and remove the scenario. Pass false after a failure. */
  finish: (verify: boolean) => Promise<void>
}

/**
 * Register one scenario and return it with its teardown.
 *
 * `fixtures.ts` wraps this as the `modelScript` fixture. It stays a plain
 * function so the helper's own unit test does not need a browser.
 */
export async function startModelScript(serverURL: string, options: ModelScriptOptions = {}): Promise<ModelScriptLifecycle> {
  const id = `test-${randomUUID()}`
  // A scenario with no step is legal: `registerMockModelScenario` always adds
  // the housekeeping rules, and every step arrives later through `queue`.
  await registerMockModelScenario(serverURL, id, { steps: [] })
  let unconsumedReason: string | undefined
  let queued = 0

  const script: ModelScript = {
    id,
    prompt: text => mockScenarioPrompt(id, text),
    queue: async (...steps) => {
      await extendMockModelScenario(serverURL, id, { steps })
      queued += steps.length
    },
    rule: (...rules) => extendMockModelScenario(serverURL, id, { rules }),
    fallback: step => extendMockModelScenario(serverURL, id, { fallback: step }),
    status: () => readScenarioStatus(serverURL, id),
    waitForSteps: (count = queued, timeoutMs = STEP_WAIT_TIMEOUT_MS) => waitForSteps(serverURL, id, count, timeoutMs, options.testDeadline?.()),
    allowUnconsumed: (reason) => {
      if (!reason)
        throw new Error('allowUnconsumed needs the reason the queue stays unconsumed')
      unconsumedReason = reason
    },
  }

  return {
    script,
    finish: async (verify: boolean) => {
      if (!verify || unconsumedReason !== undefined) {
        await removeMockModelScenario(serverURL, id, { force: true })
        return
      }
      const status = await removeMockModelScenario(serverURL, id)
      if (!status)
        return
      await removeMockModelScenario(serverURL, id, { force: true })
      throw new Error(`The model script of this test is incomplete: ${describe(status)}\n${JSON.stringify(status, null, 2)}`)
    },
  }
}

/**
 * Poll the scenario until it consumed `count` steps.
 *
 * The wait ends at `timeoutMs`, or STEP_WAIT_REPORT_MARGIN_MS before the test's own
 * deadline, whichever comes first.
 */
async function waitForSteps(
  serverURL: string,
  id: string,
  count: number,
  timeoutMs: number,
  testDeadline: number | undefined,
): Promise<MockModelScenarioStatus> {
  const started = Date.now()
  const beforeTestEnds = testDeadline === undefined ? Infinity : testDeadline - STEP_WAIT_REPORT_MARGIN_MS
  const deadline = Math.min(started + timeoutMs, beforeTestEnds)
  // The limit that applied, stated as a number, so the message is the same on every run.
  const limit = deadline === beforeTestEnds
    ? `${Math.max(0, deadline - started)}ms, before the test's own timeout`
    : `${timeoutMs}ms`
  let status = await readScenarioStatus(serverURL, id)
  while (status.nextStep < count) {
    if (Date.now() >= deadline)
      throw new Error(`The model script reached ${status.nextStep} of ${count} answers in ${limit}: ${describe(status)}`)
    await new Promise(resolve => setTimeout(resolve, 50))
    status = await readScenarioStatus(serverURL, id)
  }
  return status
}

function describe(status: MockModelScenarioStatus): string {
  const unexpected = status.unexpectedRequests.length
  return `${status.nextStep} of ${status.stepCount} queued answers consumed, `
    + `${unexpected} request${unexpected === 1 ? '' : 's'} the script did not answer`
}

/**
 * The body of the `modelScript` Playwright fixture.
 *
 * Two test bases need it: the shared one in `../fixtures.ts`, and
 * `../process-control-fixtures.ts`, which extends `@playwright/test` directly
 * because it starts its own hub. A hub a test starts sends its agents to the
 * SAME mock endpoint, because `hubSpawnEnv` gives it the same agent
 * configuration — so both read the endpoint from the run state rather than from
 * a fixture, and the two bases share one implementation.
 */
export async function runModelScriptFixture(
  use: (script: ModelScript) => Promise<void>,
  testInfo: {
    status?: string
    expectedStatus?: string
    /** The test's timeout in milliseconds; 0 when it has none. */
    timeout?: number
    outputPath?: (name: string) => string
    attach?: (name: string, options: { path: string, contentType: string }) => Promise<void>
  },
  /** When the test's timer started (the `testStartedAt` fixture). */
  testStartedAt?: number,
): Promise<void> {
  const lifecycle = await startModelScript(getGlobalState().mockModelUrl, {
    testDeadline: () => testStartedAt !== undefined && testInfo.timeout !== undefined && testInfo.timeout > 0
      ? testStartedAt + testInfo.timeout
      : undefined,
  })
  try {
    await use(lifecycle.script)
  }
  finally {
    const passed = testInfo.status === testInfo.expectedStatus
    // Attach the script BEFORE the teardown removes it. Every request body is
    // in there, tools and all, which is what names why a provider did something
    // other than what the script asked for.
    if (!passed)
      await attachScriptStatus(lifecycle.script, testInfo)
    // Do not assert consumption over a failure that already happened: the
    // incomplete script is the consequence, and the first failure is the cause.
    await lifecycle.finish(passed)
  }
}

async function attachScriptStatus(
  script: ModelScript,
  testInfo: {
    outputPath?: (name: string) => string
    attach?: (name: string, options: { path: string, contentType: string }) => Promise<void>
  },
): Promise<void> {
  if (!testInfo.outputPath || !testInfo.attach)
    return
  try {
    const path = testInfo.outputPath('model-script.json')
    writeFileSync(path, JSON.stringify(await script.status(), null, 2))
    await testInfo.attach('model-script', { path, contentType: 'application/json' })
  }
  catch {
    // A diagnostic that fails must not replace the failure it documents.
  }
}
