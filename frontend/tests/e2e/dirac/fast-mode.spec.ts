import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { anthropicDiracTest } from './fixtures'

anthropicDiracTest.use({ anthropicModel: 'claude-opus-5' })

anthropicDiracTest('enables and disables native fast serving with the model and effort fixed', async ({ anthropicDiracWorkspace, page, modelScript }) => {
  const context = {
    page,
    modelScript,
    leapmuxServer: anthropicDiracWorkspace.server,
    workspaceId: anthropicDiracWorkspace.workspaceId,
    provider: AgentProvider.DIRAC,
    textStep: (text: string) => ({ toolCalls: [diracRespondToolCall('dirac-speed-complete', 'complete', text)] }),
  }
  await waitForSettingsHydrated(page)
  const before = await currentNativeAgent(context)
  const effort = before.optionGroups.find(group => group.id === 'reasoning_effort' || group.id === 'effort')
  if (!effort || !effort.currentValue)
    throw new Error('The native Dirac effort catalog is absent.')
  expect(effort.currentValue).toBe('medium')
  await chooseSettingsOption(page, 'inference_speed-fast')
  await waitForSettingsIdle(page)
  const fast = await sendNativeAnswer(context, 'Complete once at fast serving speed.', 'The fast answer completed.')
  expect(fast.protocol).toBe('anthropic-messages')
  expect(fast.body).toMatchObject({ model: 'claude-opus-5', speed: 'fast' })
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === effort.id)?.currentValue).toBe(effort.currentValue)

  await chooseSettingsOption(page, 'inference_speed-standard')
  await waitForSettingsIdle(page)
  const standard = await sendNativeAnswer(context, 'Complete once at standard serving speed.', 'The standard answer completed.')
  expect(standard.body).toMatchObject({ model: 'claude-opus-5' })
  if (!isObject(standard.body))
    throw new Error('The native speed request is not an object.')
  expect(standard.body).not.toHaveProperty('speed')
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === effort.id)?.currentValue).toBe(effort.currentValue)
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'inference_speed-standard')
})
