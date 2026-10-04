import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { junieAnswerToolCall, junieSubmitPlanToolCall } from '../helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from '../helpers/subagentRegistry'
import {
  chooseSettingsOption,
  expectSettingsChip,
  sendMessage,
  waitForAgentIdle,
  waitForControlBanner,
  waitForSettingsHydrated,
} from '../helpers/ui'
import { expect } from '../junie-fixtures'

const PLAN = '- Inspect the repository.\n- Apply the change.\n- Report the result.\n'

const DELIVERY_PLAN = [
  { name: 'Inspect the repository', description: 'Read the repository files.' },
  { name: 'Apply the change', description: 'Make the requested change.' },
]

function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Plan task' } },
  ]
}

/** Exercise the actual native control and retain every original assertion. */
export async function exerciseNativePlanReview(context: NativeScenarioContext, options: { selectMode?: boolean, callPrefix?: string } = {}): Promise<MockModelRequestRecord> {
  const { page, modelScript } = context

  await waitForSettingsHydrated(page)
  if (options.selectMode !== false)
    await chooseSettingsOption(page, 'permissionMode-plan')
  await expectSettingsChip(page, 'Plan')

  await modelScript.rule(...junieHousekeeping())
  const start = (await modelScript.status()).stepCount
  const prefix = options.callPrefix ?? 'junie'
  await modelScript.queue(
    { toolCalls: [junieSubmitPlanToolCall(`${prefix}-create-plan`, 'plan-the-change', [{ name: 'Requirements', content: PLAN }], DELIVERY_PLAN)] },
    { toolCalls: [junieAnswerToolCall(`${prefix}-after-plan`, 'The plan is ready to implement.')] },
  )
  await sendMessage(page, modelScript.prompt('Plan the change.'))
  await modelScript.waitForSteps(start + 1)

  // The native review request carries the plan and asks whether to implement it.
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Implement this plan?')
  await expect(banner).toContainText('Inspect the repository')
  // Approve the plan and let Junie publish its delivery stages.
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 120_000)
  await expect(page.getByText('The plan is ready to implement.').filter({ visible: true }).first()).toBeVisible()
  await expect(goalsAndTodosSection(page)).toBeVisible()
  await expandGoalsAndTodosSection(page)
  const list = page.locator('[data-testid="goals-and-todos"]:visible')
  await expect(list).toContainText('Inspect the repository')
  await expect(list).toContainText('Apply the change')

  const next = (await modelScript.status()).requests.find(request => request.stepIndex === start)
  if (!next)
    throw new Error('The related native scenario reached no model request.')
  return next
}
