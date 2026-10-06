import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest('applies thinking independently of a fixed native model and effort before and after reload', async ({ authenticatedKiroWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'model-kiro-e2e-thinking')
  await waitForSettingsIdle(page)
  await chooseSettingsOption(page, 'effortLevel-medium')
  await waitForSettingsIdle(page)
  // Kiro 2.24 offers the `thinking` option, with the values `on` and `off`, only
  // for a model whose catalog schema lists both `adaptive` and `disabled` as
  // `thinking.type`. Its request then states `adaptive` for On and `disabled`
  // for Off, and never `enabled`.
  const thinking = (await currentNativeAgent(context)).optionGroups.find(group => group.id === 'thinking')
  expect(thinking?.mutable).toBe(true)
  expect(thinking?.options).toHaveLength(2)
  const enabled = thinking?.options.find(option => option.id === 'on')
  const disabled = thinking?.options.find(option => option.id === 'off')
  if (!enabled || !disabled)
    throw new Error('The native Kiro catalog exposes no independent thinking choices.')
  for (const [choice, expected, reload] of [
    [disabled, 'disabled', false],
    [enabled, 'adaptive', false],
    [enabled, 'adaptive', true],
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
