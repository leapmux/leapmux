import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { openSettingsMenu, waitForNativeSettingsHydrated } from '../helpers/ui'
import { nativeContext } from './scenarios'

fastAgentTest('exposes no native plan option', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await sendNativeAnswer(context, 'Complete the native mode catalog probe.', 'The native mode catalog probe completed.')
  await waitForNativeSettingsHydrated(page)
  const agent = await currentNativeAgent(context)
  const mode = agent.optionGroups.find(group => group.id === 'permissionMode')
  if (!mode || mode.options.length === 0)
    throw new Error('The native mode catalog is absent.')
  expect(mode.options.map(option => option.id)).not.toContain('plan')
  const menu = await openSettingsMenu(page, 'permissionMode')
  await expect(menu.getByTestId('permissionMode-plan')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.reload()
  await waitForNativeSettingsHydrated(page)
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.options.map(option => option.id)).not.toContain('plan')
})
