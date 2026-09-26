import { junieAnswerToolCall, junieCreatePlanToolCall, junieReportPlanToolCall } from './helpers/providerToolCalls'
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

/**
 * The statement that opens Junie's explore-plan subagent system prompt. A rule
 * on it answers the explore-plan turn alone, so the subagent reports the plan
 * while the main agent's own requests never match.
 */
const EXPLORE_PLAN_SYSTEM = 'READ-ONLY UNTIL PLAN'

/** Housekeeping turns every Junie task answers before the main agent runs. */
function junieHousekeeping() {
  return [
    { name: 'junie-capability-filter', when: { system: 'capability filter agent' }, respond: { text: '' } },
    { name: 'junie-task-name', when: { system: 'task description summarizer' }, respond: { text: 'Plan task' } },
  ]
}

junieTest.describe('Junie plan review', () => {
  // Plan mode is a `mode` config option (matrix note 27). `create_plan` runs
  // Junie's explore-plan subagent, which reports the plan. Junie then raises its
  // own plan-review request and fills the to-do sidebar with the plan's entries.
  junieTest('a plan raises a review request and the plan entries in the to-do sidebar', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await modelScript.rule(...junieHousekeeping())
    // The explore-plan subagent reports the plan; Junie turns it into the review
    // request and the sidebar entries.
    await modelScript.rule({
      name: 'the explore-plan subagent reports the plan',
      when: { system: EXPLORE_PLAN_SYSTEM },
      respond: { toolCalls: [junieReportPlanToolCall('junie-report-plan', PLAN)] },
    })
    await modelScript.queue(
      { toolCalls: [junieCreatePlanToolCall('junie-create-plan')] },
      { toolCalls: [junieAnswerToolCall('junie-after-plan', 'The plan is ready to implement.')] },
    )
    await sendMessage(page, modelScript.prompt('Plan the change.'))
    await modelScript.waitForSteps(1)

    // The plan-review request is a request of its own. It carries the plan and
    // asks whether to implement it.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Implement this plan?')
    await expect(banner).toContainText('Inspect the repository')

    // The to-do sidebar holds the plan's entries: Junie sends no separate to-do
    // update (matrix note 27).
    await expect(goalsAndTodosSection(page)).toBeVisible()
    await expandGoalsAndTodosSection(page)
    const list = page.locator('[data-testid="goals-and-todos"]:visible')
    await expect(list).toContainText('Inspect the repository')
    await expect(list).toContainText('Apply the change')

    // Approve the plan and the turn continues with the answer.
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(page.getByText('The plan is ready to implement.').first()).toBeVisible()
  })
})
