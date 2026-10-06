import { DIRAC_AGENT, diracTest, expect } from '../dirac-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { expectNoControlBanner, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { PLAN_REVIEW_BUTTON_TEST_IDS } from '../helpers/unsupportedPlanMode'
import { openProviderAgent } from '../helpers/workspace'
import { closeAgentViaAPI } from '../helpers/worktree'
import { withDiracPlanReadiness } from './planReadiness'

diracTest.describe('Dirac plan mode', () => {
  diracTest('the plan card resolves at the next prompt and raises no approval', async ({ page, authenticatedEmptyWorkspace, approvalDisabledDiracHome, leapmuxServer, modelScript }, testInfo) => {
    void approvalDisabledDiracHome
    const home = leapmuxServer.agentEnv.HOME
    const nodePath = findBinary('node', leapmuxServer.agentEnv)
    if (!home || !nodePath)
      throw new Error('The Dirac plan test requires a private home and Node executable.')
    await withDiracPlanReadiness({ home, nodePath, runDir: getGlobalState().tmpDir }, async (readiness) => {
      const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT, { optionValues: { permissionMode: 'plan' } })
      await withCleanup(async () => {
        await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
        await waitForSettingsHydrated(page)
        await expectSettingsChip(page, 'Plan')

        const start = await modelScript.queue(
          { toolCalls: [diracRespondToolCall('dirac-plan', 'plan', '1. Write the parser.\n2. Test it.')] },
          { toolCalls: [diracRespondToolCall('dirac-plan-revised', 'plan', '1. Write the parser.\n2. Test it.\n3. Review the parser.')] },
        )
        await sendMessage(page, modelScript.prompt('Plan the work.'))
        await modelScript.waitForSteps(start + 1)
        await waitForAgentIdle(page)

        await expect(messageBubbles(page).filter({ hasText: 'Write the parser.' }).first()).toBeVisible()
        // The deferred native plan creates no approval request, and no Allow or plan review button.
        await expectNoControlBanner(page)
        for (const testId of ['control-allow-btn', ...PLAN_REVIEW_BUTTON_TEST_IDS])
          await expect(page.getByTestId(testId)).toHaveCount(0)
        await readiness.waitForInput()
        await testInfo.attach('native Dirac plan readiness', { path: readiness.signalPath, contentType: 'application/json' })

        await sendMessage(page, modelScript.prompt('Proceed with the plan.'))
        await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        const nextRequest = await modelScript.requestAt(start + 1)
        expect(JSON.stringify(nextRequest.body)).toContain('Proceed with the plan.')
        expect(nativeToolResult(nextRequest, 'dirac-plan')).toContain('Proceed with the plan.')
        await expect(messageBubbles(page).filter({ hasText: 'Review the parser.' }).first()).toBeVisible()
        await expectNoControlBanner(page)
      }, async () => {
        const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agentId)
        expect(closed.failureMessage).toBe('')
      })
    })
  })
})
