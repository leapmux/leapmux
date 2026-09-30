import { junieAnswerToolCall, junieSubmitPlanToolCall } from './helpers/providerToolCalls'
import { expandGoalsAndTodosSection, goalsAndTodosSection } from './helpers/subagentRegistry'
import {
  chooseSettingsOption,
  expectSettingsChip,
  sendMessage,
  waitForAgentIdle,
  waitForControlBanner,
  waitForSettingsHydrated,
} from './helpers/ui'
import { expect, JUNIE_E2E_SKIP_REASON, junieTest } from './junie-fixtures'

junieTest.skip(!!JUNIE_E2E_SKIP_REASON, JUNIE_E2E_SKIP_REASON || '')

const PLAN = '- Inspect the repository.\n- Apply the change.\n- Report the result.\n'
const DELIVERY_PLAN = [
  { name: 'Inspect the repository', description: 'Read the repository files.' },
  { name: 'Apply the change', description: 'Make the requested change.' },
]

/** Housekeeping turns every Junie task answers before the main agent runs. */
function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Plan task' } },
  ]
}

junieTest.describe('Junie plan review', () => {
  // Plan mode is a `mode` config option (matrix note 27). `submit` sends the
  // proposal tabs and delivery stages to Junie's plan review.
  junieTest('a plan raises a review request and the plan entries in the to-do sidebar', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await modelScript.rule(...junieHousekeeping())
    await modelScript.queue(
      { toolCalls: [junieSubmitPlanToolCall('junie-create-plan', 'plan-the-change', [{ name: 'Requirements', content: PLAN }], DELIVERY_PLAN)] },
      { toolCalls: [junieAnswerToolCall('junie-after-plan', 'The plan is ready to implement.')] },
    )
    await sendMessage(page, modelScript.prompt('Plan the change.'))
    await modelScript.waitForSteps(1)

    // The plan-review request is a request of its own. It carries the plan and
    // asks whether to implement it.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Implement this plan?')
    await expect(banner).toContainText('Inspect the repository')
    // Approve the plan and let Junie publish its delivery stages.
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(page.getByText('The plan is ready to implement.').first()).toBeVisible()
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('Apply the change')
  })
})
