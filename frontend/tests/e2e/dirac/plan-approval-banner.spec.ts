import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/worktree'
import { nativeContext } from './scenarios'

diracTest('publishes a native deferred plan without a dedicated approval banner', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: async () => {
    await chooseSettingsOption(page, 'permissionMode-plan')
    const start = (await modelScript.status()).stepCount
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-deferred-plan', 'plan', '- DIRACDEFERREDPLAN inspect the file.')] })
    await sendMessage(page, modelScript.prompt('Return the scripted proposal.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'DIRACDEFERREDPLAN' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  } })
  const agent = await currentNativeAgent(context)
  const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agent.id)
  expect(closed.failureMessage).toBe('')
})
