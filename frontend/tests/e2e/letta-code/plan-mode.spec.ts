import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { openSettingsMenu, waitForSettingsHydrated } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('exposes no native plan option', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await sendNativeAnswer(context, 'Complete the native mode catalog probe.', 'The native mode catalog probe completed.')
  await waitForSettingsHydrated(page)
  const agent = await currentNativeAgent(context)
  const mode = agent.optionGroups.find(group => group.id === 'permissionMode')
  if (!mode || mode.options.length === 0)
    throw new Error('The native mode catalog is absent.')
  expect(mode.options.map(option => option.id)).not.toContain('plan')
  const menu = await openSettingsMenu(page, 'permissionMode')
  await expect(menu.getByTestId('permissionMode-plan')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.reload()
  await waitForSettingsHydrated(page)
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.options.map(option => option.id)).not.toContain('plan')
})
