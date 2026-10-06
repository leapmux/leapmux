import { expect } from '@playwright/test'
import { FASTAGENT_MODE } from '../../../src/generated/contracts/fastagent-protocol'
import { fastAgentTest } from '../fastagent-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelInstructionText, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsOptionChosen, waitForSettingsHydrated } from '../helpers/ui'
import { fastAgentModesTest } from './fixtures'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent settings', () => {
  fastAgentTest('the settings menu shows the agent mode', async ({ authenticatedFastAgentWorkspace, page }) => {
    void authenticatedFastAgentWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
  })
})

fastAgentModesTest('selects another configured native agent before and after reload', async ({ fastAgentModesWorkspace, page, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer: fastAgentModesWorkspace.server, workspaceId: fastAgentModesWorkspace.workspaceId })
  expect(nativeOptionValue(await currentNativeAgent(context), 'permissionMode')).toBe('reader')
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
  fastAgentTest('keeps the chosen agent mode after reload', async ({ authenticatedFastAgentWorkspace, page }) => {
    void authenticatedFastAgentWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
    await page.reload()
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
  })
})
