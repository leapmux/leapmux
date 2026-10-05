import type { Fixtures } from '@playwright/test'
import type { MockModelRequestRecord, MockModelRule, MockModelScenarioStatus, MockModelStep } from './mockModelScript'
import { randomUUID } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { withCleanup } from './cleanup'
import {
  extendMockModelScenario,
  mockScenarioPrompt,
  readScenarioStatus,
  registerMockModelScenario,
  releaseMockModelGate,
  releaseMockModelGateIfHeld,
  removeMockModelScenario,
} from './mockModelScenario'
import { describeScenarioStatus, stepRequest, validateGateName, validateStepIndex } from './mockModelScript'
import { getGlobalState } from './server'
import { currentTestDeadline, startTestDeadline, WAIT_REPORT_MARGIN_MS } from './testDeadline'

/** Long enough for an agent process to start and run a turn on a loaded machine. */
const STEP_WAIT_TIMEOUT_MS = 180_000

/** Options of `startModelScript`. */
export interface ModelScriptOptions {
  /**
   * Read the whole-test deadline in epoch milliseconds.
   * Return undefined when the test has no timeout.
   * Each wait reads the current value, so a timeout increase before that wait extends its deadline.
   */
  testDeadline?: () => number | undefined
  /**
   * Keep the whole status of a script that ends incomplete. The failure message holds a summary only.
   * A failure in this hook never replaces the incomplete-script failure.
   */
  attachStatus?: (status: MockModelScenarioStatus) => Promise<void>
}

/**
 * One model script belongs to one test.
 * The test scripts its answers and marks each prompt.
 * The fixture checks exact script consumption at teardown.
 * An unscripted turn fails the test that caused it, which protects the next test on the shared process.
 */
export interface ModelScript {
  /** The scenario identifier, which also appears in a failure attachment. */
  id: string
  /** Read the current whole-test deadline in epoch milliseconds, or undefined when the test has no timeout. */
  testDeadline: () => number | undefined
  /** Mark a prompt so the requests it causes reach this test's script. */
  prompt: (text: string) => string
  /**
   * Append answers to the ordered queue, in the order the agent will ask.
   * Return the step index of the first appended answer. Measure each later step from that index, never from 0:
   * an earlier turn of the same test, or a fallback, can consume steps before this one.
   * Concurrent calls append in call order, so each call returns the index of its own first answer.
   */
  queue: (...steps: MockModelStep[]) => Promise<number>
  /**
   * Wait until the agent requests the ordered step `stepIndex`, then return the record of that request.
   * A wait that ends before the request arrives, or a record that the server dropped, fails with the script state.
   * A native client can add fields to a record after the mock counts its step.
   * A caller that needs the final record calls this after the turn ends.
   */
  requestAt: (stepIndex: number) => Promise<MockModelRequestRecord>
  /**
   * Add a rule that answers matching requests without consuming an ordered step.
   * Use a rule when the test cannot predict a turn's position or count:
   *
   * - A retry.
   * - A provider summary.
   * - A subagent with an unknown turn count.
   */
  rule: (...rules: MockModelRule[]) => Promise<void>
  /**
   * Use this step to answer requests after the ordered queue ends.
   * Without a fallback, an unscripted turn fails the test.
   * Use a fallback only when the native turn count cannot be determined.
   * Examples include an approval that restarts the agent and a provider that retries by itself.
   * State the reason at the call site.
   */
  fallback: (step: MockModelStep) => Promise<void>
  /** The live consumption state. */
  status: () => Promise<MockModelScenarioStatus>
  /**
   * Wait until the agent consumes the requested number of queued answers.
   * By default, count includes every answer queued so far.
   * The mock counts an ordered step when the agent requests it.
   * This proves that the native model request arrived.
   * The thinking indicator can stay absent during startup, before that request arrives.
   * A wait on that absent indicator can return before the turn begins and leave the transcript empty.
   */
  waitForSteps: (count?: number, timeoutMs?: number) => Promise<MockModelScenarioStatus>
  /** Wait until a scripted model request stops at gate. */
  waitForGate: (gate: string, timeoutMs?: number) => Promise<MockModelScenarioStatus>
  /** Release the model requests that wait at gate. */
  releaseGate: (gate: string) => Promise<void>
  /** Clean up a held or cancelled response without a status-read race. */
  releaseGateIfHeld: (gate: string) => Promise<boolean>
  /**
   * Accept an unconsumed queue at teardown for the stated reason.
   * Use this only when the test ends a turn before the agent requests every answer.
   * Examples include an interrupt and a Worker restart during a turn.
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
  // registerMockModelScenario adds housekeeping rules even when the ordered queue has no steps.
  // Later queue calls add those steps.
  await registerMockModelScenario(serverURL, id, { steps: [] })
  let unconsumedReason: string | undefined
  // Only this fixture appends steps to its scenario, so this count equals the server's step count.
  let queued = 0
  // Append in call order. Two concurrent requests could otherwise reach the server in the other order,
  // and each call would return the first index of the other call's steps.
  let lastAppend: Promise<unknown> = Promise.resolve()

  const script: ModelScript = {
    id,
    testDeadline: () => options.testDeadline?.(),
    prompt: text => mockScenarioPrompt(id, text),
    queue: (...steps) => {
      const appended = lastAppend.then(async () => {
        const first = queued
        await extendMockModelScenario(serverURL, id, { steps })
        queued += steps.length
        return first
      })
      // A failed append rejects its own call. A later call still runs, and it reads the unchanged count.
      lastAppend = appended.catch(() => {})
      return appended
    },
    requestAt: async (stepIndex) => {
      validateStepIndex(stepIndex)
      return stepRequest(await script.waitForSteps(stepIndex + 1), stepIndex)
    },
    rule: (...rules) => extendMockModelScenario(serverURL, id, { rules }),
    fallback: step => extendMockModelScenario(serverURL, id, { fallback: step }),
    status: () => readScenarioStatus(serverURL, id),
    waitForSteps: (count = queued, timeoutMs = STEP_WAIT_TIMEOUT_MS) => waitForSteps(serverURL, id, count, timeoutMs, options.testDeadline?.()),
    waitForGate: (gate, timeoutMs = STEP_WAIT_TIMEOUT_MS) => waitForGate(serverURL, id, gate, timeoutMs, options.testDeadline?.()),
    releaseGate: gate => releaseMockModelGate(serverURL, id, gate),
    releaseGateIfHeld: gate => releaseMockModelGateIfHeld(serverURL, id, gate),
    allowUnconsumed: (reason) => {
      if (!reason || !reason.trim())
        throw new Error('The allowUnconsumed call needs the reason that the queue stays unconsumed.')
      unconsumedReason = reason
    },
  }

  return {
    script,
    finish: async (verify: boolean) => {
      if (!verify) {
        await removeMockModelScenario(serverURL, id, { force: true })
        return
      }
      let cleanupNeeded = true
      return withCleanup(async () => {
        const status = await removeMockModelScenario(serverURL, id, { allowUnconsumed: unconsumedReason !== undefined })
        if (!status) {
          cleanupNeeded = false
          return
        }
        await options.attachStatus?.(status).catch(() => {})
        throw new Error(`The model script of this test is incomplete: ${describeScenarioStatus(status)}\n${JSON.stringify(summarize(status), null, 2)}`)
      }, async () => {
        if (cleanupNeeded)
          await removeMockModelScenario(serverURL, id, { force: true })
      })
    },
  }
}

/**
 * Poll the scenario until it consumes count ordered steps.
 * Stop at the earlier of the caller limit and the whole-test deadline minus WAIT_REPORT_MARGIN_MS.
 */
async function waitForSteps(
  serverURL: string,
  id: string,
  count: number,
  timeoutMs: number,
  testDeadline: number | undefined,
): Promise<MockModelScenarioStatus> {
  const { deadline, limit } = waitDeadline(timeoutMs, testDeadline)
  let status = await readScenarioStatus(serverURL, id)
  while (status.nextStep < count) {
    if (Date.now() >= deadline)
      throw new Error(`The model script reached ${status.nextStep} of ${count} answers in ${limit}: ${describeScenarioStatus(status)}`)
    await new Promise(resolve => setTimeout(resolve, 50))
    status = await readScenarioStatus(serverURL, id)
  }
  return status
}

/** Poll the scenario until a real model request waits at gate. */
async function waitForGate(
  serverURL: string,
  id: string,
  gate: string,
  timeoutMs: number,
  testDeadline: number | undefined,
): Promise<MockModelScenarioStatus> {
  validateGateName(gate)
  const { deadline, limit } = waitDeadline(timeoutMs, testDeadline)
  let status = await readScenarioStatus(serverURL, id)
  while (!status.pendingGates.includes(gate)) {
    if (Date.now() >= deadline)
      throw new Error(`The model script did not hold gate ${gate} in ${limit}: ${describeScenarioStatus(status)}`)
    await new Promise(resolve => setTimeout(resolve, 50))
    status = await readScenarioStatus(serverURL, id)
  }
  return status
}

/** Apply the test deadline to a model-script wait. */
function waitDeadline(timeoutMs: number, testDeadline: number | undefined): { deadline: number, limit: string } {
  const started = Date.now()
  const beforeTestEnds = testDeadline === undefined ? Infinity : testDeadline - WAIT_REPORT_MARGIN_MS
  const deadline = Math.min(started + timeoutMs, beforeTestEnds)
  // Keep the limit in the error so it states which deadline ended the wait.
  const limit = deadline === beforeTestEnds
    ? `${Math.max(0, deadline - started)}ms, before the test's own timeout`
    : `${timeoutMs}ms`
  return { deadline, limit }
}

/** The most entries of one list that a failure message shows. */
const SUMMARY_LIST_LIMIT = 20

/**
 * Reduce a status to what a failure message needs: the counts, the rule matches, and the route of each request.
 * A request body holds the whole conversation and every tool schema, and a long goal loop grows it with each turn.
 * A message that held 200 bodies reached 169 MB. Playwright copies a failure message into each report,
 * and the copies exhausted the 4 GB heap of the shard process. The full status goes to an attachment.
 */
function summarize(status: MockModelScenarioStatus): Record<string, unknown> {
  const limit = <T, U>(list: T[], pick: (item: T) => U): { count: number, shown: U[] } => ({
    count: list.length,
    shown: list.slice(0, SUMMARY_LIST_LIMIT).map(pick),
  })
  return {
    complete: status.complete,
    nextStep: status.nextStep,
    stepCount: status.stepCount,
    ruleMatches: status.ruleMatches,
    pendingGates: status.pendingGates,
    requests: limit(status.requests, ({ protocol, path, stepIndex, rule, fallback }) => ({ protocol, path, stepIndex, rule, fallback })),
    unexpectedRequests: limit(status.unexpectedRequests, ({ protocol, path, reason }) => ({ protocol, path, reason })),
  }
}

/** The fixtures of {@link modelScriptFixtures}. */
export interface ModelScriptFixtures {
  /** When the test's timer started, in epoch milliseconds. */
  testStartedAt: number
  modelScript: ModelScript
}

/**
 * The model script and the test deadline. Both Playwright test bases spread this object into their `extend` call:
 *
 * - ../fixtures.ts uses the shared suite Hub.
 * - ../process-control-fixtures.ts extends @playwright/test and owns its process-control Hub.
 *
 * Playwright starts a test's timer before fixture setup. `testStartedAt` is automatic and has no dependencies, so it
 * records that start before every other fixture runs. A wait that must end before the test's own timeout reads the
 * deadline through `./testDeadline`: the model-script waits, and `waitForAgentIdle`.
 */
export const modelScriptFixtures: Fixtures<ModelScriptFixtures> = {
  // eslint-disable-next-line no-empty-pattern
  testStartedAt: [async ({}, use, testInfo) => {
    const startedAt = Date.now()
    const end = startTestDeadline(startedAt, () => testInfo.timeout)
    try {
      await use(startedAt)
    }
    finally {
      end()
    }
  }, { auto: true }],

  // Give each test one model script. A marked prompt belongs to that script.
  // An unscripted turn fails its test. An unmarked prompt reaches the ambient
  // scenario, which answers native title turns and refuses other content turns.
  // eslint-disable-next-line no-empty-pattern
  modelScript: async ({}, use, testInfo) => runModelScriptFixture(use, testInfo),
}

/**
 * Run one test's model script.
 * Both Hubs use hubSpawnEnv to send agents to the same isolated mock server.
 * Read that endpoint from the run state for both test bases.
 */
export async function runModelScriptFixture(
  use: (script: ModelScript) => Promise<void>,
  testInfo: {
    status?: string
    expectedStatus?: string
    outputPath?: (name: string) => string
    attach?: (name: string, options: { path: string, contentType: string }) => Promise<void>
  },
): Promise<void> {
  const lifecycle = await startModelScript(getGlobalState().mockModelUrl, {
    testDeadline: currentTestDeadline,
    // A failed test attaches the live status above. A passed test reaches this hook when its script ends incomplete.
    attachStatus: status => attachStatusFile(status, testInfo),
  })
  try {
    await use(lifecycle.script)
  }
  finally {
    const passed = testInfo.status === testInfo.expectedStatus
    // Attach the script before teardown removes it.
    // The recorded native request bodies show which tools and model turns reached the mock.
    if (!passed)
      await attachScriptStatus(lifecycle.script, testInfo)
    // Do not replace the test failure with a script-consumption failure.
    // The incomplete script can result from the original test failure.
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
  try {
    await attachStatusFile(await script.status(), testInfo)
  }
  catch {
    // A diagnostic that fails must not replace the failure it documents.
  }
}

/** Write the whole status beside the test report. The file is compact, because a status can hold megabytes. */
async function attachStatusFile(
  status: MockModelScenarioStatus,
  testInfo: {
    outputPath?: (name: string) => string
    attach?: (name: string, options: { path: string, contentType: string }) => Promise<void>
  },
): Promise<void> {
  if (!testInfo.outputPath || !testInfo.attach)
    return
  const path = testInfo.outputPath('model-script.json')
  writeFileSync(path, JSON.stringify(status))
  await testInfo.attach('model-script', { path, contentType: 'application/json' })
}
