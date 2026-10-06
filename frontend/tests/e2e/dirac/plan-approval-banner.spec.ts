import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectNoControlBanner, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { closeAgentViaAPI } from '../helpers/worktree'
import { nativeContext } from './scenarios'

diracTest('publishes a native deferred plan without a dedicated approval banner', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await expectNoPlanReview(context, { relatedProof: async () => {
    await chooseSettingsOption(page, 'permissionMode-plan')
    const start = await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-deferred-plan', 'plan', '- DIRACDEFERREDPLAN inspect the file.')] })
    await sendMessage(page, modelScript.prompt('Return the scripted proposal.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)
    await expect(messageContents(page).filter({ hasText: 'DIRACDEFERREDPLAN' }).first()).toBeVisible()
    await expectNoControlBanner(page)
  } })
  const agent = await currentNativeAgent(context)
  const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agent.id)
  expect(closed.failureMessage).toBe('')
})
