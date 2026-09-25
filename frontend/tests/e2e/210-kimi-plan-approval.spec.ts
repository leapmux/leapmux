import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { enterPlanModeToolCall, exitPlanModeFromFileToolCall, writeToolCall } from './helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, KIMI_PLAN_FILE_CAPTURE, kimiTest, occurrences, stepRequestBody } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

const PLAN = '# Kimi plan\n\n- Option A: do it the simple way.\n- Option B: do it the robust way.\n'

const APPROACHES = [
  { label: 'Option A', description: 'Simple' },
  { label: 'Option B', description: 'Robust' },
]

/**
 * The two steps that raise the plan for review.
 *
 * Kimi Code chooses the plan file path at random and raises the plan from that
 * file, so the model writes to the path that the capture reads from the
 * request, then leaves plan mode.
 */
function planSteps() {
  return [
    {
      toolCalls: [writeToolCall(KIMI, 'write-plan', { path: '{{planFile}}', content: PLAN })],
      captures: { ...KIMI_PLAN_FILE_CAPTURE },
    },
    { toolCalls: [exitPlanModeFromFileToolCall(KIMI, 'exit-plan', APPROACHES)] },
  ]
}

/** The joined bodies of the requests that the script answered at `from` or later. */
async function requestsFrom(script: ModelScript, from: number): Promise<string> {
  const status = await script.status()
  return status.requests.filter(request => (request.stepIndex ?? -1) >= from).map(request => JSON.stringify(request.body)).join('\n')
}

/** Open the banner's overflow menu and choose one plan choice. */
async function choosePlanChoice(page: Page, index: number) {
  await page.getByTestId('control-more-actions').click()
  await page.getByTestId(`plan-choice-${index}`).click()
}

kimiTest.describe('reviews a Kimi Code plan', () => {
  kimiTest('an approved approach leaves plan mode and reaches the model', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(...planSteps(), { text: 'Executing Option B.' })
    await sendMessage(page, modelScript.prompt('Plan the change and offer two approaches.'))
    await modelScript.waitForSteps(2)

    // The request carries the plan itself, so the banner draws it.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Option B: do it the robust way.')
    await page.getByTestId('control-more-actions').click()
    await expect(page.getByTestId('plan-choice-0')).toContainText('Approve: Option A')
    await expect(page.getByTestId('plan-choice-1')).toContainText('Approve: Option B')
    await page.getByTestId('plan-choice-1').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(await requestsFrom(modelScript, 2)).toContain('Selected approach: Option B')
    // A plan approval opens on the Smart preset, which is Ask When Needed for
    // Kimi Code. The server leaves plan mode in that mode, and the worker
    // follows it.
    await expectSettingsChip(page, 'Ask When Needed')
  })

  // EnterPlanMode states the plan file path in its own result, so the capture
  // reads it from there.
  kimiTest('a request for revisions keeps plan mode, and the model hears it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Always Ask')

    await modelScript.queue(
      { toolCalls: [enterPlanModeToolCall(KIMI, 'enter-plan')] },
      ...planSteps(),
      { text: 'I will revise the plan.' },
    )
    await sendMessage(page, modelScript.prompt('Enter plan mode, plan the change, and offer two approaches.'))
    await modelScript.waitForSteps(3)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expectSettingsChip(page, 'Plan')
    // The choices are Approve A, Approve B, Request revisions, and Reject.
    await choosePlanChoice(page, 2)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // Every request carries the tool schemas and the plan-mode reminder, and either
    // can speak of a revision. So the answer reached the model only when the request
    // after the plan review speaks of it more often than the request before it.
    const revisions = (step: number) => occurrences(stepRequestBody(status.requests, step).toLowerCase(), 'revis')
    expect(revisions(3), 'the plan result asks for a revision').toBeGreaterThan(revisions(2))
    await expectSettingsChip(page, 'Plan')
  })
})
