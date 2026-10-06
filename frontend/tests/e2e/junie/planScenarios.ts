import type { MockModelRequestRecord, MockModelStep } from '../helpers/mockModelScript'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { junieAnswerToolCall, junieSubmitPlanToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import {
  chooseSettingsOption,
  controlBanner,
  expectSettingsChip,
  savedControlAnswer,
  sendMessage,
  waitForAgentIdle,
  waitForControlBanner,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from '../helpers/ui'
import { expect } from '../junie-fixtures'
import { JUNIE_PLAN_REPLY_RULE } from './planReplyRule'
import { planWithheldToolsOffered } from './planToolCatalog'

const PLAN = '- Inspect the repository.\n- Apply the change.\n- Report the result.\n'

const DELIVERY_PLAN = [
  { name: 'Inspect the repository', description: 'Read the repository files.' },
  { name: 'Apply the change', description: 'Make the requested change.' },
]

/**
 * Prove that a request used Junie's planning tool catalog: it submits a plan and offers no tool that
 * Plan mode withholds. See `PLAN_MODE_WITHHELD_TOOLS` for the catalogs, and for why `multi_edit` is
 * absent from that list.
 */
export function expectNativePlanToolCatalog(request: MockModelRequestRecord): void {
  const tools = nativeModelToolNames(request)
  expect(tools).toContain('submit')
  expect(planWithheldToolsOffered(tools), 'Plan mode withholds these tools of the Default catalog').toEqual([])
}

function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Plan task' } },
  ]
}

interface PlanPromptOptions {
  selectMode?: boolean
  callPrefix?: string
  housekeepingRegistered?: boolean
}

function submitPlanStep(prefix: string): MockModelStep {
  return { toolCalls: [junieSubmitPlanToolCall(`${prefix}-create-plan`, 'plan-the-change', [{ name: 'Requirements', content: PLAN }], DELIVERY_PLAN)] }
}

/**
 * Send the planning prompt and wait for the native review of the plan that Junie submits.
 *
 * The first queued step must be the submit call. Return the index of that step.
 */
async function sendPlanPrompt(context: NativeScenarioContext, options: PlanPromptOptions, steps: MockModelStep[]): Promise<number> {
  const { page, modelScript } = context

  await waitForSettingsHydrated(page)
  if (options.selectMode !== false) {
    await chooseSettingsOption(page, 'permissionMode-plan')
    // Junie applies the mode with a live write, so the prompt must follow the Worker's reply.
    await waitForSettingsIdle(page)
  }
  await expectSettingsChip(page, 'Plan')

  if (!options.housekeepingRegistered)
    await modelScript.rule(...junieHousekeeping())
  const start = (await modelScript.status()).stepCount
  await modelScript.queue(...steps)
  await sendMessage(page, modelScript.prompt('Plan the change.'))
  await modelScript.waitForSteps(start + 1)

  // The native review request carries the plan and asks whether to implement it.
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Implement this plan?')
  await expect(banner).toContainText('Inspect the repository')
  return start
}

async function readModelRequest(context: NativeScenarioContext, stepIndex: number): Promise<MockModelRequestRecord> {
  const request = (await context.modelScript.status()).requests.find(candidate => candidate.stepIndex === stepIndex)
  if (!request)
    throw new Error('The related native scenario reached no model request.')
  return request
}

/**
 * Exercise the actual native control and retain every original assertion.
 *
 * Each call registers the two housekeeping rules of Junie's own side requests, and the model script
 * accepts a rule name once. A test that calls this function again with the same script must pass
 * `housekeepingRegistered: true`.
 *
 * Approval ends Plan mode in Junie, and Junie keeps the approved plan in the session. In some runs,
 * Junie then answers the next planning prompt with the question "You already have a plan in this
 * session" and waits for a reply. A probe of Junie 26.9.22 showed the question in 5 of 26 runs. Junie
 * sends no model request for that prompt until a reply arrives. A test must not plan a second time
 * after an approval in the same session. To plan twice, call `exerciseNativePlanRevision` first.
 */
export async function exerciseNativePlanReview(
  context: NativeScenarioContext,
  options: PlanPromptOptions = {},
): Promise<MockModelRequestRecord> {
  const { page, modelScript } = context

  const prefix = options.callPrefix ?? 'junie'
  const start = await sendPlanPrompt(context, options, [
    submitPlanStep(prefix),
    { toolCalls: [junieAnswerToolCall(`${prefix}-after-plan`, 'The plan is ready to implement.')] },
  ])
  // Approve the plan and let Junie publish its delivery stages.
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expect(page.getByText('The plan is ready to implement.').filter({ visible: true }).first()).toBeVisible()
  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible')
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Apply the change')

  return readModelRequest(context, start)
}

/**
 * Send a planning prompt in Plan mode and deny the plan.
 *
 * Deny selects Junie's first reject option, `revise`. Junie keeps the plan pending, ends the turn
 * with no further model request, and stays in Plan mode. The caller must select Plan mode before
 * this call.
 *
 * Junie then treats the next planning prompt as a reply to the pending plan. It asks a classifier
 * model about that prompt, and the rule of this helper answers `NO`. Junie plans again. In 17 of 17
 * probe runs of Junie 26.9.22, it did not ask "You already have a plan in this session" after a
 * denied plan.
 * The housekeeping rules follow the same contract as in `exerciseNativePlanReview`.
 */
export async function exerciseNativePlanRevision(
  context: NativeScenarioContext,
  options: Omit<PlanPromptOptions, 'selectMode'> = {},
): Promise<MockModelRequestRecord> {
  const { page, modelScript } = context

  await modelScript.rule(JUNIE_PLAN_REPLY_RULE)
  const start = await sendPlanPrompt(context, { ...options, selectMode: false }, [submitPlanStep(options.callPrefix ?? 'junie')])
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await expect(controlBanner(page)).toHaveCount(0)
  // The browser draws the saved answer from the Worker row alone, so the Worker took the answer.
  await expect(savedControlAnswer(page)).toHaveCount(1)
  await waitForAgentIdle(page)
  await expectSettingsChip(page, 'Plan')
  const status = await modelScript.status()
  expect(status.unexpectedRequests).toEqual([])
  expect(status.nextStep, 'Junie sends no model request after a denied plan').toBe(start + 1)

  return readModelRequest(context, start)
}
