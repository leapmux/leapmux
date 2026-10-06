import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { kimiModelContextText } from './modelContextText'

kimiTest('applies independent swarm mode to native context and preserves it after reload', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }) => {
  // The reader joins the raw message text: the generic JSON reader escapes the
  // quotes of the native reminder, so `You are now in "agent swarm" mode.`
  // would never match.
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE, readModelContext: kimiModelContextText }
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'swarmMode-on')
  await waitForSettingsIdle(page)
  const enabled = await sendNativeAnswer(context, 'Reply once under the current independent setting.', 'The enabled native setting reached the model.')
  expect(kimiModelContextText(enabled)).toContain('You are now in "agent swarm" mode.')
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsOptionChosen(page, 'swarmMode-on')
  const restored = await sendNativeAnswer(context, 'Reply once after restoring this setting.', 'The restored native setting reached the model.')
  expect(kimiModelContextText(restored)).toContain('You are now in "agent swarm" mode.')
  await chooseSettingsOption(page, 'swarmMode-off')
  await waitForSettingsIdle(page)
  const disabled = await sendNativeAnswer(context, 'Reply once after changing this independent setting.', 'The disabled native setting reached the model.')
  const exitReminder = 'Swarm Mode has ended.'
  expect(kimiModelContextText(disabled).split(exitReminder).length).toBeGreaterThan(kimiModelContextText(restored).split(exitReminder).length)
  await expectSettingsOptionChosen(page, 'swarmMode-off')
})
