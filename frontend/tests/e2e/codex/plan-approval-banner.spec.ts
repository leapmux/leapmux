import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { isResizeObserverLoopError } from '~/lib/ignorableErrorEvents'
import { codexTest } from '../codex-fixtures'
import { answerControl, chooseSettingsOption, controlButton, enterControlFeedback, expectSettingsChip, sendMessage, visibleOnly, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

const PLAN_BODY = 'This is a dummy plan for testing the coding agent plan mode UI. Never execute this plan.'

const INITIAL_PLAN_PROMPT
  = `I am testing the Codex plan mode UI. Stay in plan mode and reply with a concise markdown plan whose title is "# Dummy plan" and whose body includes "${PLAN_BODY}". Do not implement anything yet.`

const REVISE_PLAN_PROMPT
  = 'Please revise the plan. Keep the title "# Dummy plan revised" and include the exact sentence "Add tests before implementation." Do not implement anything yet.'

/**
 * Wrap a plan the way Codex's own plan-mode instructions require.
 *
 * Its developer message states it exactly: present the official plan wrapped in
 * a `proposed_plan` block, with the content starting on the next line. Only a
 * plan in those tags becomes the `plan` item that `providers/codex/output.go` turns into
 * the approval request. Plain markdown arrives as an ordinary agent message and
 * raises no banner at all.
 */
function proposedPlan(body: string): string {
  return `<proposed_plan>\n${body}\n</proposed_plan>`
}

async function configureCodexPlanMode(page: Page) {
  await chooseSettingsOption(page, 'collaboration_mode-plan')
  await expectSettingsChip(page, 'GPT-5.6-Luna')
  await expectSettingsChip(page, 'Plan Mode')
}

codexTest.describe('Codex Plan Mode Prompt', () => {
  codexTest('feedback revises the plan and approval can clear context', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    const pageErrors: string[] = []
    page.on('pageerror', error => pageErrors.push(error.message))

    await configureCodexPlanMode(page)

    // Codex raises its plan from the model's TEXT, not from a tool call, so the
    // plan is simply what the script answers. The marker rides in the plan
    // because an approved plan restarts the agent on a session seeded from it.
    await modelScript.fallback({ text: 'Working through the approved plan.' })
    const planned = await modelScript.queue({ text: proposedPlan(modelScript.prompt(`# Dummy plan\n\n${PLAN_BODY}`)) })
    await sendMessage(page, modelScript.prompt(INITIAL_PLAN_PROMPT))
    await modelScript.waitForSteps(planned + 1)
    await waitForAgentIdle(page)

    // Plan content is rendered with plan styling (ToolUseLayout with "Proposed Plan" title).
    // Use .last() because getByText also matches the user prompt which contains PLAN_BODY.
    await expect(visibleOnly(page.getByRole('heading', { name: 'Dummy plan' }))).toBeVisible()
    await expect(visibleOnly(page.getByText(PLAN_BODY)).last()).toBeVisible()

    // The visible copy of each plan control. A count of zero visible copies proves that no copy of the control is
    // visible.
    const control = (testId: string) => page.getByTestId(testId).filter({ visible: true })
    const firstBanner = await waitForControlBanner(page)
    await expect(firstBanner.getByText('Plan Ready for Review')).toBeVisible()
    await expect(controlButton(page, 'deny')).toHaveText('Reject')
    await expect(controlButton(page, 'allow')).toHaveText('Approve')
    await expect(control('plan-clear-context-checkbox')).toBeVisible()
    await expect(control('control-permissions-pill-group')).toBeVisible()

    await enterControlFeedback(page, REVISE_PLAN_PROMPT)
    await expect(controlButton(page, 'deny')).toHaveText('Send feedback')
    await expect(controlButton(page, 'allow')).toHaveCount(0)
    await expect(control('plan-clear-context-checkbox')).toHaveCount(0)
    await expect(control('control-permissions-pill-group')).toHaveCount(0)
    const revised = await modelScript.queue({ text: proposedPlan(modelScript.prompt('# Dummy plan revised\n\nAdd tests before implementation.')) })
    await answerControl(page, 'deny')
    await modelScript.waitForSteps(revised + 1)

    await waitForAgentIdle(page)

    // Revised plan content appears with plan styling.
    // Use .last() because getByText also matches the revision prompt.
    await expect(visibleOnly(page.getByRole('heading', { name: 'Dummy plan revised' }))).toBeVisible()
    await expect(visibleOnly(page.getByText('Add tests before implementation.')).last()).toBeVisible()

    const revisedBanner = await waitForControlBanner(page)
    await expect(revisedBanner.getByText('Plan Ready for Review')).toBeVisible()

    const clearContextSwitch = control('plan-clear-context-checkbox').locator('input[type="checkbox"]')
    await expect(clearContextSwitch).not.toBeChecked()
    await clearContextSwitch.check()
    await answerControl(page, 'allow')
    // Scoped to the prompt's own text, not to the banner slot. Executing the
    // approved plan can raise the NEXT control request into that same slot,
    // which says nothing about whether this approval cleared.
    await expect(revisedBanner.getByText('Plan Ready for Review')).not.toBeVisible()
    await expectSettingsChip(page, 'Default')
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
    await expect(visibleOnly(page.getByText('Execute plan'))).toBeVisible()
    // Every uncaught page error, not one known message. A narrow regex passes
    // for a crash whose wording changed, and for every unrelated crash in this
    // flow. `ignorableErrorEvents` owns which messages are browser-inherent, so
    // this asks it rather than spelling its regex a second time.
    expect(pageErrors.filter(message => !isResizeObserverLoopError(message))).toEqual([])
  })
})
