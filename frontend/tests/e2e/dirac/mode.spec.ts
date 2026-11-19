import { rmSync } from 'node:fs'
import { expect } from '@playwright/test'
import { DIRAC_MODE } from '../../../src/generated/contracts/dirac-protocol'
import { diracTest } from '../dirac-fixtures'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { ruleRequest } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { captureStoredNativeModeNotice, chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { getGlobalState, hubSpawnEnv } from '../helpers/server'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, messageContents, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/workerTabs'
import { openProviderAgent } from '../helpers/workspace'
import { diracActModeContinuationRule, diracCurrentModeDetails } from './modeContinuation'
import { withDiracPlanReadiness } from './planReadiness'
import { DIRAC_AGENT, nativeContext } from './scenarios'

diracTest.describe('Dirac settings', () => {
  diracTest('the mode menu lists Plan and Act', async ({ authenticatedDiracWorkspace, page }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(DIRAC_MODE))
    await expectSettingsOptionChosen(page, `permissionMode-${DIRAC_MODE.Act}`)
  })
})

diracTest.describe('Dirac settings apply', () => {
  diracTest('sends plan and act modes in successive native requests', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)

    await applyPermissionPreset(page, 'bypass')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await modelScript.rule(diracActModeContinuationRule('dirac-mode-change-resume', {
      toolCalls: [
        diracRespondToolCall('dirac-act-answer', 'complete', 'The Act check ended.'),
      ],
    }))
    const planStep = await modelScript.queue({
      toolCalls: [
        diracRespondToolCall('dirac-plan-answer', 'plan', 'The Plan check ended.'),
      ],
    })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    const planned = await modelScript.requestAt(planStep)
    expect(planned.body).toMatchObject({ reasoning_effort: 'high' })
    const planDetails = diracCurrentModeDetails(planned)
    expect(planDetails).toContain('# Current Mode\nPLAN MODE')
    expect(planDetails).not.toContain('# Current Mode\nACT MODE')

    await expect.poll(async () => (await modelScript.status()).ruleMatches['dirac-mode-change-resume'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Act')
    const status = await modelScript.status()
    const actDetails = diracCurrentModeDetails(ruleRequest(status, 'dirac-mode-change-resume'))
    expect(actDetails).toContain('# Current Mode\nACT MODE')
    expect(actDetails).not.toContain('# Current Mode\nPLAN MODE')
  })

  diracTest('returns through the menu from a restored native Plan to Act', async ({ page, authenticatedEmptyWorkspace, approvalDisabledDiracHome, leapmuxServer, modelScript }) => {
    void approvalDisabledDiracHome
    const home = leapmuxServer.agentEnv.HOME
    if (!home)
      throw new Error('The Dirac mode test requires a private home.')
    const nodePath = requireBinary('node', 'The Dirac mode readiness probe requires the Node.js runtime', hubSpawnEnv(leapmuxServer.agentEnv))
    await withDiracPlanReadiness({ home, nodePath, runDir: getGlobalState().tmpDir }, async (readiness) => {
      const workspaceId = authenticatedEmptyWorkspace.workspaceId
      const { agentId } = await openProviderAgent(leapmuxServer, workspaceId, DIRAC_AGENT)
      await withCleanup(async () => {
        await openWorkspace(page, workspaceId)
        const context = await nativeContext({
          page,
          modelScript,
          leapmuxServer,
          workspaceId,
        })
        await waitForSettingsHydrated(page)
        await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(DIRAC_MODE))
        const planNotice = await chooseNativeModeWithNotice(context, {
          groupId: 'permissionMode',
          value: DIRAC_MODE.Plan,
          label: 'Plan',
          previousLabel: 'Act',
          noticeLabel: 'Mode',
        })

        for (const restored of [false, true]) {
          if (restored) {
            await page.reload()
            await waitForSettingsHydrated(page)
            await expectSettingsChip(page, 'Plan')
            await expectSettingsOptionChosen(page, `permissionMode-${DIRAC_MODE.Plan}`)
          }
          const callId = restored ? 'dirac-restored-plan-mode' : 'dirac-selected-plan-mode'
          const planStep = await modelScript.queue({
            toolCalls: [
              diracRespondToolCall(callId, 'plan', 'Inspect the files before the change.'),
            ],
          })
          await sendMessage(page, modelScript.prompt(restored ? 'Revise the plan after reload.' : 'Plan the change in the selected mode.'))
          await modelScript.waitForSteps(planStep + 1)
          await waitForAgentIdle(page)
          const details = diracCurrentModeDetails(await modelScript.requestAt(planStep))
          expect(details).toContain('# Current Mode\nPLAN MODE')
          expect(details).not.toContain('# Current Mode\nACT MODE')
          await readiness.waitForInput()
          rmSync(readiness.signalPath)
          await planNotice.assertStored()
        }

        // The native mode change resumes the pending Plan before a new prompt.
        const resumeRule = 'dirac-menu-act-resume'
        await modelScript.rule(diracActModeContinuationRule(resumeRule, {
          toolCalls: [
            diracRespondToolCall('dirac-menu-act-resumed', 'complete', 'The native Act mode resumed the plan.'),
          ],
        }))
        const ownerBefore = await currentNativeAgent(context)
        expect(ownerBefore.optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe(DIRAC_MODE.Plan)
        const requestsBefore = (await modelScript.status()).requests.length
        await chooseSettingsOption(page, 'permissionMode-act')
        await waitForSettingsIdle(page)
        await expectSettingsChip(page, 'Act')
        await expect.poll(async () => (await modelScript.status()).ruleMatches[resumeRule] ?? 0).toBe(1)
        await waitForAgentIdle(page)
        const status = await modelScript.status()
        expect(status.requests).toHaveLength(requestsBefore + 1)
        expect(status.requests.at(-1)?.rule).toBe(resumeRule)
        const resumed = ruleRequest(status, resumeRule)
        expect(diracCurrentModeDetails(resumed)).toContain('# Current Mode\nACT MODE')
        expect(diracCurrentModeDetails(resumed)).not.toContain('# Current Mode\nPLAN MODE')
        const notice = messageContents(page).filter({ hasText: 'Mode (Plan → Act)' })
        await expect(notice).toHaveCount(1)
        const actNotice = await captureStoredNativeModeNotice(context, ownerBefore, {
          groupId: 'permissionMode',
          oldValue: DIRAC_MODE.Plan,
          value: DIRAC_MODE.Act,
          label: 'Act',
          previousLabel: 'Plan',
          noticeLabel: 'Mode',
        }, notice)
        const act = await sendNativeAnswer(context, 'Continue in the menu-selected Act mode.', 'The menu selected native Act.')
        const actDetails = diracCurrentModeDetails(act)
        expect(actDetails).toContain('# Current Mode\nACT MODE')
        expect(actDetails).not.toContain('# Current Mode\nPLAN MODE')

        await page.reload()
        await waitForSettingsHydrated(page)
        await expectSettingsChip(page, 'Act')
        await expectSettingsOptionChosen(page, `permissionMode-${DIRAC_MODE.Act}`)
        const restoredAct = await sendNativeAnswer(context, 'Reply after restoring the menu-selected Act mode.', 'The restored native Act mode answered.')
        expect(diracCurrentModeDetails(restoredAct)).toContain('# Current Mode\nACT MODE')
        expect(diracCurrentModeDetails(restoredAct)).not.toContain('# Current Mode\nPLAN MODE')
        await planNotice.assertStored()
        await actNotice.assertStored()
      }, async () => {
        const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agentId)
        expect(closed.failureMessage).toBe('')
      })
    })
  })
})
