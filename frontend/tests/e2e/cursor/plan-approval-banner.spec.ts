import { expect } from '@playwright/test'
import { CURSOR_METHOD } from '../../../src/generated/contracts/cursor-protocol'
import { cursorTest } from '../cursor-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { cursorCreatePlanToolCall } from '../helpers/providerToolCalls'
import { answerControl, assistantBubbles, chooseSettingsOption, controlBanner, expectSettingsOptionChosen, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

cursorTest('approves a native create-plan request', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  // Cursor answers its own Run with the create-plan result, so the turn is one model step.
  const start = await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
    'cursor-plan',
    'Review changes',
    'Review without edits.',
    '# Plan\n\n1. Inspect the files.',
  )] })
  await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Review changes')
  await expect(banner).toContainText('Inspect the files')
  // Cursor's plan control draws its Approve action on the shared Allow button.
  await answerControl(page, 'allow')

  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'Cursor plan accepted' }).first()).toBeVisible()
})

// cursor-agent answers its own Run stream with the create-plan result, and the mock
// Cursor service ends the turn with that result. So the turn needs no second model
// answer. cursor-agent fills a rejection that carries no reason with the words
// "User rejected plan" (create-plan-handler.ts in the installed bundle), and it does
// not change its mode for a plan answer, in either direction.
cursorTest('rejects a native create-plan request and keeps the saved decision after reload', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  const agent = await waitForNativeOptionApplied(native, 'permissionMode', 'plan')
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    const start = await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
      'cursor-rejected-plan',
      'Keep planning',
      'Keep this plan unapproved.',
      '# Plan\n\n1. Review the change.',
    )] })
    await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Keep planning')
    await expect(banner).toContainText('Review the change')
    const observed = await waitForOneNativeControl(watch)
    // Cursor's plan control draws its Reject action on the shared Deny button.
    await answerControl(page, 'deny')

    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    const result = assistantBubbles(page).filter({ hasText: 'Cursor plan rejected: User rejected plan' })
    await expect(result.first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await expectSettingsOptionChosen(page, 'permissionMode-plan')
    const { decision } = await readObservedNativeDecision(native, agent, watch, observed)
    expect(decision.request.method).toBe(CURSOR_METHOD.CreatePlan)
    // A bare rejection carries no reason field, which is what lets cursor-agent supply its own.
    expect(decision.response).toEqual({ jsonrpc: '2.0', id: observed.payload.id, result: { outcome: { outcome: 'rejected' } } })

    await page.reload()
    await expect(result.first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
  }, async () => watch.cancel())
})
