import { expect } from '@playwright/test'
import { currentNativeAgent, expectNativeOptionValue, nativeModelContextText } from '../helpers/nativeScenario'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, controlBanner, enterControlFeedback, savedControlAnswer, sendMessage, visibleOnly, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('approves the streamed plan and exits Plan mode', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  const plan = '# Reasonix approval plan\n\n1. Inspect the parser.\n2. Report the result.'
  const start = await modelScript.queue({ text: plan }, { text: 'The approved plan is executing.' })
  await sendMessage(page, modelScript.prompt('Plan the parser inspection, then ask for approval.'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await expect(banner).toContainText('The agent requests approval to proceed.')
  await expect(assistantBubbles(page).filter({ hasText: 'Inspect the parser.' }).first()).toBeVisible()
  await answerPlanReview(page, 'approve')
  await expect(banner).toHaveCount(0)
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expectNativeOptionValue(native, 'permissionMode', 'normal')
  expect(nativeModelContextText(await modelScript.requestAt(start + 1))).toContain(plan)
  await expect(assistantBubbles(page).filter({ hasText: 'The approved plan is executing.' }).first()).toBeVisible()
  await page.reload()
  await expect(savedControlAnswer(page)).toHaveText('Approve')
  await expectNativeOptionValue(native, 'permissionMode', 'normal')
})

reasonixTest('rejects the plan with revision feedback and keeps planning', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  const feedback = 'Split the parser inspection into two separate steps.'
  const start = await modelScript.queue(
    { text: '# Reasonix rejected plan\n\n1. Inspect the parser.' },
    { text: '# Revised plan\n\n1. Inspect the lexer.\n2. Inspect the parser.' },
  )
  await sendMessage(page, modelScript.prompt('Plan the parser inspection, then ask for approval.'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await enterControlFeedback(page, feedback)
  await answerPlanReview(page, 'reject')
  await expect(banner).toHaveCount(0)
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expectNativeOptionValue(native, 'permissionMode', 'plan')
  expect(nativeModelContextText(await modelScript.requestAt(start + 1))).toContain(feedback)
  await expect(assistantBubbles(page).filter({ hasText: 'Inspect the lexer.' }).first()).toBeVisible()
  await page.reload()
  await expect(savedControlAnswer(page)).toHaveText('Reject')
  await expectNativeOptionValue(native, 'permissionMode', 'plan')
})

reasonixTest('executes the saved plan in a fresh context', async ({ native }) => {
  const { page, modelScript } = native
  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  const before = await currentNativeAgent(native)
  const plan = modelScript.prompt('# Reasonix fresh-context plan\n\n1. Preserve this exact proposal.')
  await modelScript.rule({
    name: 'the saved plan executes in the fresh context',
    when: { body: 'Execute the following plan' },
    respond: { text: 'The saved plan executed in a fresh context.' },
  })
  const start = await modelScript.queue({ text: plan })
  await sendMessage(page, modelScript.prompt('Plan the fresh-context probe, then ask for approval.'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await page.locator('[data-testid="plan-clear-context-checkbox"]:visible input[type="checkbox"]').check()
  await answerPlanReview(page, 'approve')
  await expect(banner).toHaveCount(0)
  await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'The saved plan executed in a fresh context.' }).first()).toBeVisible()
  await waitForAgentIdle(page)
  const status = await modelScript.status()
  expect(status.ruleMatches['the saved plan executes in the fresh context']).toBe(1)
  const execution = status.requests.find(request => nativeModelContextText(request).includes('Execute the following plan'))
  expect(execution).toBeDefined()
  expect(nativeModelContextText(execution!)).toContain(plan)
  const after = await currentNativeAgent(native)
  expect(after.id).toBe(before.id)
  expect(after.agentSessionId).not.toBe(before.agentSessionId)
})
