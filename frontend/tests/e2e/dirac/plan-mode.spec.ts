import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from '../dirac-fixtures'
import { findBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/worktree'
import { withDiracPlanReadiness } from './planReadiness'

diracTest.describe('Dirac plan mode', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('the plan card resolves at the next prompt and raises no approval', async ({ page, authenticatedEmptyWorkspace, approvalDisabledDiracHome, leapmuxServer, modelScript }, testInfo) => {
    void approvalDisabledDiracHome
    const home = leapmuxServer.agentEnv.HOME
    const nodePath = findBinary('node', leapmuxServer.agentEnv)
    if (!home || !nodePath)
      throw new Error('The Dirac plan test requires a private home and Node executable.')
    await withDiracPlanReadiness({ home, nodePath, runDir: getGlobalState().tmpDir }, async (readiness) => {
      const { agentId } = await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'plan' })
      await withCleanup(async () => {
        await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
        await waitForSettingsHydrated(page)
        await expectSettingsChip(page, 'Plan')

        await modelScript.queue(
          { toolCalls: [diracRespondToolCall('dirac-plan', 'plan', '1. Write the parser.\n2. Test it.')] },
          { toolCalls: [diracRespondToolCall('dirac-plan-revised', 'plan', '1. Write the parser.\n2. Test it.\n3. Review the parser.')] },
        )
        await sendMessage(page, modelScript.prompt('Plan the work.'))
        await modelScript.waitForSteps(1)
        await waitForAgentIdle(page, 120_000)

        await expect(messageBubbles(page).filter({ hasText: 'Write the parser.' }).first()).toBeVisible()
        // The deferred native plan creates no approval request or Approve button.
        await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
        await expect(page.locator('[data-testid="control-allow-btn"]')).toHaveCount(0)
        await readiness.waitForInput()
        await testInfo.attach('native Dirac plan readiness', { path: readiness.signalPath, contentType: 'application/json' })

        await sendMessage(page, modelScript.prompt('Proceed with the plan.'))
        const status = await modelScript.waitForSteps()
        await waitForAgentIdle(page, 120_000)
        const nextRequest = status.requests.find(request => request.stepIndex === 1)
        expect(JSON.stringify(nextRequest?.body)).toContain('Proceed with the plan.')
        expect(nativeToolResult(nextRequest, 'dirac-plan')).toContain('Proceed with the plan.')
        await expect(messageBubbles(page).filter({ hasText: 'Review the parser.' }).first()).toBeVisible()
        await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
      }, async () => {
        const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agentId)
        expect(closed.failureMessage).toBe('')
      })
    })
  })
})
