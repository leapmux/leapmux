import { expect } from '@playwright/test'
import { CURSOR_METHOD } from '../../../src/generated/contracts/cursor-protocol'
import { cursorTest } from '../cursor-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { cursorCreatePlanToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, savedControlAnswer, sendMessage, visibleControlBanner, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

cursorTest('approves a native create-plan request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
    'cursor-plan',
    'Review changes',
    'Review without edits.',
    '# Plan\n\n1. Inspect the files.',
  )] })
  await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
  await modelScript.waitForSteps()
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Review changes')
  await expect(banner).toContainText('Inspect the files')
  await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

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
cursorTest('rejects a native create-plan request and keeps the saved decision after reload', async ({ authenticatedCursorWorkspace, leapmuxServer, page, modelScript }) => {
  void authenticatedCursorWorkspace
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  const agent = await waitForNativeOptionApplied({ page, leapmuxServer }, 'permissionMode', 'plan')
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    await modelScript.queue({ toolCalls: [cursorCreatePlanToolCall(
      'cursor-rejected-plan',
      'Keep planning',
      'Keep this plan unapproved.',
      '# Plan\n\n1. Review the change.',
    )] })
    await sendMessage(page, modelScript.prompt('Write a plan and ask for approval.'))
    await modelScript.waitForSteps()
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Keep planning')
    await expect(banner).toContainText('Review the change')
    await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
    const observed = onlyObservedNativeControl(watch.controls())
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    const result = assistantBubbles(page).filter({ hasText: 'Cursor plan rejected: User rejected plan' })
    await expect(result.first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await expectSettingsOptionChosen(page, 'permissionMode-plan')
    expect(onlyObservedNativeControl(watch.controls())).toBe(observed)
    const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
    const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
    expect(decision.request).toEqual(observed.payload)
    expect(decision.request.method).toBe(CURSOR_METHOD.CreatePlan)
    // A bare rejection carries no reason field, which is what lets cursor-agent supply its own.
    expect(decision.response).toEqual({ jsonrpc: '2.0', id: observed.payload.id, result: { outcome: { outcome: 'rejected' } } })

    await page.reload()
    await expect(result.first()).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
  }, async () => watch.cancel())
})
