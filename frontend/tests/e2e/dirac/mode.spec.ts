import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { rmSync } from 'node:fs'
import { expect } from '@playwright/test'
import { DIRAC_MODE } from '../../../src/generated/contracts/dirac-protocol'
import { isObject } from '../../../src/lib/jsonPick'
import { diracTest } from '../dirac-fixtures'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { ruleRequest } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { getGlobalState, hubSpawnEnv } from '../helpers/server'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/workerTabs'
import { openProviderAgent } from '../helpers/workspace'
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
  function nativeCurrentMode(request: MockModelRequestRecord): string {
    expect(request.protocol).toBe('openai-chat-completions')
    const body = isObject(request.body)
      ? request.body
      : null
    const messages = Array.isArray(body?.messages)
      ? body.messages
      : []
    // Dirac adds no environment details when feedback revises a pending Plan. Its latest explicit mode still controls that turn.
    const texts = messages.filter(isObject).filter(message => message.role === 'user').flatMap((message) => {
      if (typeof message.content === 'string')
        return [message.content]
      if (!Array.isArray(message.content))
        return []
      return message.content.filter(isObject).flatMap(part => typeof part.text === 'string'
        ? [part.text]
        : [])
    })
    return texts.findLast(text => text.startsWith('<environment_details>\n# Current Mode\n')) ?? ''
  }

  diracTest('sends plan and act modes in successive native requests', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'permissionMode-plan')
    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await modelScript.rule({
      name: 'dirac-mode-change-resume',
      when: { user: '^<environment_details>\\n# Current Mode\\nACT MODE' },
      respond: {
        toolCalls: [
          diracRespondToolCall('dirac-act-answer', 'complete', 'The Act check ended.'),
        ],
      },
      once: true,
    })
    const planStep = await modelScript.queue({
      toolCalls: [
        diracRespondToolCall('dirac-plan-answer', 'plan', 'The Plan check ended.'),
      ],
    })
    await sendMessage(page, modelScript.prompt('Reply once in the selected mode.'))
    const planned = await modelScript.requestAt(planStep)
    expect(planned.body).toMatchObject({ reasoning_effort: 'high' })
    const planDetails = nativeCurrentMode(planned)
    expect(planDetails).toContain('# Current Mode\nPLAN MODE')
    expect(planDetails).not.toContain('# Current Mode\nACT MODE')

    await expect.poll(async () => (await modelScript.status()).ruleMatches['dirac-mode-change-resume'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Act')
    const status = await modelScript.status()
    const actDetails = nativeCurrentMode(ruleRequest(status, 'dirac-mode-change-resume'))
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
          const details = nativeCurrentMode(await modelScript.requestAt(planStep))
          expect(details).toContain('# Current Mode\nPLAN MODE')
          expect(details).not.toContain('# Current Mode\nACT MODE')
          await readiness.waitForInput()
          rmSync(readiness.signalPath)
          await planNotice.assertStored()
        }

        // ACP changes the native mode without resolving the pending Plan card. An explicit prompt resumes that Plan in Act.
        const actNotice = await chooseNativeModeWithNotice(context, {
          groupId: 'permissionMode',
          value: DIRAC_MODE.Act,
          label: 'Act',
          previousLabel: 'Plan',
          noticeLabel: 'Mode',
        })
        const act = await sendNativeAnswer(context, 'Continue in the menu-selected Act mode.', 'The menu selected native Act.')
        const actDetails = nativeCurrentMode(act)
        expect(actDetails).toContain('# Current Mode\nACT MODE')
        expect(actDetails).not.toContain('# Current Mode\nPLAN MODE')

        await page.reload()
        await waitForSettingsHydrated(page)
        await expectSettingsChip(page, 'Act')
        await expectSettingsOptionChosen(page, `permissionMode-${DIRAC_MODE.Act}`)
        const restoredAct = await sendNativeAnswer(context, 'Reply after restoring the menu-selected Act mode.', 'The restored native Act mode answered.')
        expect(nativeCurrentMode(restoredAct)).toContain('# Current Mode\nACT MODE')
        expect(nativeCurrentMode(restoredAct)).not.toContain('# Current Mode\nPLAN MODE')
        await planNotice.assertStored()
        await actNotice.assertStored()
      }, async () => {
        const closed = await closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, agentId)
        expect(closed.failureMessage).toBe('')
      })
    })
  })
})
