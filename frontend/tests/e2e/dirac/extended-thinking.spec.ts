import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { anthropicDiracTest } from './fixtures'

anthropicDiracTest('enables and disables native thinking with the model and effort fixed', async ({ anthropicDiracWorkspace, page, modelScript }) => {
  const context = {
    page,
    modelScript,
    provider: AgentProvider.DIRAC,
    textStep: (text: string) => ({ toolCalls: [diracRespondToolCall('dirac-thinking-complete', 'complete', text)] }),
  }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'thinking_budget-1024')
  await waitForSettingsIdle(page)
  const enabled = await sendNativeAnswer(context, 'Complete once with thinking enabled.', 'The thinking-enabled answer completed.')
  expect(enabled.protocol).toBe('anthropic-messages')
  expect(enabled.body).toMatchObject({
    model: 'claude-haiku-4-5-20251001',
    thinking: { type: 'adaptive' },
    output_config: { effort: 'medium' },
  })

  await chooseSettingsOption(page, 'thinking_budget-0')
  await waitForSettingsIdle(page)
  const disabled = await sendNativeAnswer(context, 'Complete once with thinking disabled.', 'The thinking-disabled answer completed.')
  expect(disabled.body).toMatchObject({ model: 'claude-haiku-4-5-20251001', output_config: { effort: 'medium' } })
  if (!isObject(disabled.body))
    throw new Error('The native thinking request is not an object.')
  expect(disabled.body).not.toHaveProperty('thinking')
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'thinking_budget-0')
  expect(anthropicDiracWorkspace.agentId).not.toBe('')
})
