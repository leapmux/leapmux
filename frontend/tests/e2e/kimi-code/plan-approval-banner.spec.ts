import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeModelBodiesAfter } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exitPlanModeFromFileToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, controlActions, expectNoControlBanner, expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_PLAN_FILE_CAPTURE } from './planScenario'

const KIMI = AgentProvider.KIMI_CODE

const PLAN = '# Kimi plan\n\n- Option A: do it the simple way.\n- Option B: do it the robust way.\n'

const APPROACHES = [
  { label: 'Option A', description: 'Simple' },
  { label: 'Option B', description: 'Robust' },
]

/**
 * Two native steps prepare the plan for review.
 * Kimi chooses a random plan file path. The capture reads that path from the request.
 * The model writes the plan to that file. It then leaves Plan mode to request review.
 */
function planSteps() {
  return [
    {
      toolCalls: [
        writeToolCall(KIMI, 'write-plan', { path: '{{planFile}}', content: PLAN }),
      ],
      captures: { ...KIMI_PLAN_FILE_CAPTURE },
    },
    {
      toolCalls: [
        exitPlanModeFromFileToolCall(KIMI, 'exit-plan', APPROACHES),
      ],
    },
  ]
}

/** Open the overflow menu of the visible control request. The composer draws it in its own fieldset, outside the banner. */
async function openPlanChoices(page: Page) {
  await controlActions(page).getByTestId('control-more-actions').click()
}

/** Locate the visible plan choice `index` of the open overflow menu. */
function planChoice(page: Page, index: number) {
  return page.getByTestId(`plan-choice-${index}`).filter({ visible: true })
}

/** Open the overflow menu of the control request and choose one plan choice. */
async function choosePlanChoice(page: Page, index: number) {
  await openPlanChoices(page)
  await planChoice(page, index).click()
}

/** Enable planning through the user control before the model writes the plan. */
async function enablePlanMode(page: Page) {
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
}

kimiTest.describe('reviews a Kimi Code plan', () => {
  kimiTest('an approved approach leaves plan mode and reaches the model', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await enablePlanMode(page)

    const start = await modelScript.queue(...planSteps(), { text: 'Executing Option B.' })
    await sendMessage(page, modelScript.prompt('Plan the change and offer two approaches.'))
    await modelScript.waitForSteps(start + 2)

    // The request carries the plan itself, so the banner draws it.
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Option B: do it the robust way.')
    await openPlanChoices(page)
    await expect(planChoice(page, 0)).toContainText('Approve: Option A')
    await expect(planChoice(page, 1)).toContainText('Approve: Option B')
    await planChoice(page, 1).click()
    await expectNoControlBanner(page)

    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    expect(nativeModelBodiesAfter(await modelScript.status(), start + 2)).toContain('Selected approach: Option B')
    // A plan approval opens on the Smart preset, which is Ask When Needed for
    // Kimi Code. The server leaves plan mode in that mode, and the worker
    // follows it.
    await expectSettingsChip(page, 'Ask When Needed')
  })

  kimiTest('a request for revisions keeps plan mode, and the model hears it', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await enablePlanMode(page)

    const start = await modelScript.queue(
      ...planSteps(),
      { text: 'I will revise the plan.' },
    )
    await sendMessage(page, modelScript.prompt('Plan the change and offer two approaches.'))
    await modelScript.waitForSteps(start + 2)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expectSettingsChip(page, 'Plan')
    // The choices are Approve A, Approve B, Request revisions, and Reject.
    await choosePlanChoice(page, 2)
    await expectNoControlBanner(page)

    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    const request = await modelScript.requestAt(start + 2)
    expect(nativeToolResult(request, 'exit-plan')).toContain('User requested revisions. Plan mode remains active.')
    await expectSettingsChip(page, 'Plan')
  })
})
