import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { antigravityTest } from '../antigravity-fixtures'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, controlBanner, enterControlFeedback, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { writeAntigravityPlan } from './scenarios'

for (const decision of ['approve', 'reject'] as const) {
  antigravityTest(`reads the complete native plan file before ${decision}`, async ({ native, page, modelScript }) => {
    await chooseSettingsOption(page, 'permissionMode-plan')
    const agent = await currentNativeAgent(native)
    const home = native.leapmuxServer.agentEnv?.HOME
    if (!home)
      throw new Error('The Antigravity plan scenario requires the isolated HOME.')
    const path = join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId, 'implementation_plan.md')
    const plan = '# Native complete plan\n\n1. Read the actual source.\n2. Apply only the approved change.\n'
    const start = await modelScript.queue(
      {
        toolCalls: [
          writeAntigravityPlan(path, plan),
        ],
      },
      { text: 'The native plan is ready.' },
      ...(decision === 'approve'
        ? [
            { text: 'The approved plan was implemented.' },
          ]
        : []),
    )
    await sendMessage(page, modelScript.prompt('Write the native plan, then request its approval.'))
    await modelScript.waitForSteps(start + 2)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Read the actual source.')
    await expect(banner).toContainText('Apply only the approved change.')
    const watch = await watchNativeControls(native.leapmuxServer, agent.id)
    try {
      await answerPlanReview(page, decision)
      await expect.poll(() => watch.controls().at(-1)?.responseState, JSON.stringify(watch.controls(), null, 2)).toBe(ControlResponseState.COMPLETED)
      await expect(banner).toHaveCount(0)
    }
    finally {
      watch.cancel()
    }
    if (decision === 'approve') {
      await modelScript.waitForSteps(start + 3)
      await expect(assistantBubbles(page).filter({ hasText: 'The approved plan was implemented.' })).toBeVisible()
    }
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `permissionMode-${decision === 'approve'
      ? 'default'
      : 'plan'}`)
  })
}

antigravityTest('requests a revision and presents one new review card for it', async ({ native, page, modelScript }) => {
  await chooseSettingsOption(page, 'permissionMode-plan')
  const agent = await currentNativeAgent(native)
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The Antigravity plan scenario requires the isolated HOME.')
  const path = join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId, 'implementation_plan.md')
  const feedback = 'Add a verification step before implementation.'
  const start = await modelScript.queue(
    {
      toolCalls: [
        writeAntigravityPlan(path, '# First plan\n\nImplement the change.\n'),
      ],
    },
    { text: 'The first plan is ready.' },
    {
      toolCalls: [
        writeAntigravityPlan(path, '# Revised plan\n\nImplement the change.\n\nVerify the change.\n'),
      ],
    },
    { text: 'The revised plan is ready.' },
    { text: 'The revised plan was implemented.' },
  )
  await sendMessage(page, modelScript.prompt('Write the native plan, then request its approval.'))
  await modelScript.waitForSteps(start + 2)
  await enterControlFeedback(page, feedback)
  await answerPlanReview(page, 'reject')
  await modelScript.waitForSteps(start + 3)
  expect(nativeModelContextText(await modelScript.requestAt(start + 2))).toContain(feedback)
  await waitForAgentIdle(page)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Proposed Plan')
  await expect(banner).toContainText('Verify the change.')
  await answerPlanReview(page, 'approve')
  await modelScript.waitForSteps(start + 5)
  await expect(assistantBubbles(page).filter({ hasText: 'The revised plan was implemented.' })).toBeVisible()
  await expectSettingsOptionChosen(page, 'permissionMode-default')
})

antigravityTest('approves with Clear Context and carries the full plan into the new cascade', async ({ native, page, modelScript }) => {
  await chooseSettingsOption(page, 'permissionMode-plan')
  const agent = await currentNativeAgent(native)
  const home = native.leapmuxServer.agentEnv?.HOME
  if (!home)
    throw new Error('The Antigravity plan scenario requires the isolated HOME.')
  const path = join(home, '.gemini', 'antigravity-cli', 'brain', agent.agentSessionId, 'implementation_plan.md')
  // The fresh cascade's first prompt is this plan, so it must carry the scenario marker.
  const plan = modelScript.prompt('# Plan for a fresh cascade\n\nRead the source.\nVerify the approved change.\n')
  const start = await modelScript.queue(
    {
      toolCalls: [
        writeAntigravityPlan(path, plan),
      ],
    },
    { text: 'The plan is ready.' },
    { text: 'The fresh cascade implemented the plan.' },
  )
  await sendMessage(page, modelScript.prompt('Write the native plan, then request its approval.'))
  await modelScript.waitForSteps(start + 2)
  await page.locator('[data-testid="plan-clear-context-checkbox"]:visible input[type="checkbox"]').check()
  await answerPlanReview(page, 'approve')
  await modelScript.waitForSteps(start + 3)
  const next = await modelScript.requestAt(start + 2)
  expect(nativeModelContextText(next)).toContain('Read the source.')
  expect(nativeModelContextText(next)).toContain('Verify the approved change.')
  await expect(page.getByText('Context cleared', { exact: true }).filter({ visible: true }).first()).toBeVisible()
  await expectSettingsOptionChosen(page, 'permissionMode-default')
  const fresh = await currentNativeAgent(native)
  expect(fresh.agentSessionId).not.toBe(agent.agentSessionId)
})
