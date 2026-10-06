import { expect, FAST_AGENT_AGENT, fastAgentTest } from '../fastagent-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { openSettingsMenu, openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { fastAgentModesTest } from './fixtures'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent settings', () => {
  fastAgentTest('the settings menu shows the agent mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page, 'permissionMode')

    const group = await openSettingsMenu(page, 'permissionMode')
    await expect(group.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
  })
})

fastAgentModesTest('selects another configured native agent before and after reload', async ({ fastAgentModesWorkspace, page, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer: fastAgentModesWorkspace.server, workspaceId: fastAgentModesWorkspace.workspaceId })
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('reader')
  const initial = await sendNativeAnswer(context, 'Reply through the initial configured reader agent.', 'The native reader agent answered.')
  expect(nativeModelInstructionText(initial)).toContain('NATIVE_FAST_AGENT_READER')
  expect(nativeModelInstructionText(initial)).not.toContain('NATIVE_FAST_AGENT_WRITER')
  await exerciseNativeOption(context, {
    groupId: 'permissionMode',
    value: 'writer',
    nativeProof: (request) => {
      const instruction = nativeModelInstructionText(request)
      expect(instruction).toContain('NATIVE_FAST_AGENT_WRITER')
      expect(instruction).not.toContain('NATIVE_FAST_AGENT_READER')
    },
  })
})

fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('keeps the chosen agent mode after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page, 'permissionMode')

    const modeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(modeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
    await page.reload()
    await waitForSettingsHydrated(page, 'permissionMode')
    const reloadedModeGroup = await openSettingsMenu(page, 'permissionMode')
    await expect(reloadedModeGroup.locator('[data-testid="permissionMode-agent"] input[type="radio"]')).toBeChecked()
  })
})
