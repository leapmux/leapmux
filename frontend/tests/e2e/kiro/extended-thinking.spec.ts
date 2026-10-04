import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('applies thinking independently of a fixed native model and effort before and after reload', async ({ authenticatedKiroWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'model-kiro-e2e-thinking')
  await waitForSettingsIdle(page)
  await chooseSettingsOption(page, 'effortLevel-medium')
  await waitForSettingsIdle(page)
  const thinking = (await currentNativeAgent(context)).optionGroups.find(group => group.id === 'thinking')
  expect(thinking?.mutable).toBe(true)
  expect(thinking?.options).toHaveLength(2)
  const enabled = thinking?.options.find(option => /^(?:on|enabled|true)$/i.test(option.id))
  const disabled = thinking?.options.find(option => /^(?:off|disabled|false)$/i.test(option.id))
  if (!enabled || !disabled)
    throw new Error('The native Kiro catalog exposes no independent thinking choices.')
  for (const [choice, expected, reload] of [
    [disabled, 'disabled', false],
    [enabled, 'enabled', false],
    [enabled, 'enabled', true],
    [disabled, 'disabled', false],
    [disabled, 'disabled', true],
  ] as const) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page)
    }
    else {
      await chooseSettingsOption(page, `thinking-${choice.id}`)
      await waitForSettingsIdle(page)
    }
    await expectSettingsOptionChosen(page, `thinking-${choice.id}`)
    const request = await sendNativeAnswer(context, 'Reply under the current thinking choice.', `The native thinking choice is ${expected}.`)
    expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', 'kiro-e2e-thinking')
    expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', 'medium')
    expect(request.body).toHaveProperty('additionalModelRequestFields.thinking.type', expected)
  }
})
