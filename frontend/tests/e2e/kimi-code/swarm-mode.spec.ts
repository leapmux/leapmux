import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('applies independent swarm mode to native context and preserves it after reload', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'swarmMode-on')
  await waitForSettingsIdle(page)
  const enabled = await sendNativeAnswer(context, 'Reply once under the current independent setting.', 'The enabled native setting reached the model.')
  expect(nativeModelContextText(enabled)).toContain('You are now in "agent swarm" mode.')
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'swarmMode-on')
  const restored = await sendNativeAnswer(context, 'Reply once after restoring this setting.', 'The restored native setting reached the model.')
  expect(nativeModelContextText(restored)).toContain('You are now in "agent swarm" mode.')
  await chooseSettingsOption(page, 'swarmMode-off')
  await waitForSettingsIdle(page)
  const disabled = await sendNativeAnswer(context, 'Reply once after changing this independent setting.', 'The disabled native setting reached the model.')
  const exitReminder = 'Swarm Mode has ended.'
  expect(nativeModelContextText(disabled).split(exitReminder).length).toBeGreaterThan(nativeModelContextText(restored).split(exitReminder).length)
  await expectSettingsOptionChosen(page, 'swarmMode-off')
})
