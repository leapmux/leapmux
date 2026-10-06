import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, closeComposerMenus, expectNoControlBanner, expectSettingsOptionChosen, openPlusMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
import { expectQoderModeChip, nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest('the mode menu lists the five modes, and a switch survives a reload', async ({ authenticatedQoderWorkspace, page }) => {
    void authenticatedQoderWorkspace
    await waitForSettingsHydrated(page)

    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(QODER_MODE))
    // The fixture opens the agent in Accept Edits.
    await expectSettingsOptionChosen(page, `permissionMode-${QODER_MODE.AcceptEdits}`)

    await chooseSettingsOption(page, `permissionMode-${QODER_MODE.Auto}`)
    await waitForSettingsIdle(page)
    await expectQoderModeChip(page, 'Auto')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Auto')
    await expectSettingsOptionChosen(page, `permissionMode-${QODER_MODE.Auto}`)
    // The model axis is present; the account decides which models it lists.
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)
  })

  qoderTest('asks in Default and denies a write in Don\'t Ask', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
    const file = join(askingQoderWorkspace.workingDir, 'qoder-mode-write.txt')
    const command = 'printf qoder-mode-write > ./qoder-mode-write.txt'
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Default')

    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.QODER, 'default-mode-write', command),
      decision: 'deny',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('qoder-mode-write.txt')
        expect(existsSync(file)).toBe(false)
      },
      nativeProof: (request) => {
        expect(nativeToolResult(request, 'default-mode-write')).toMatch(/denied|rejected|not allowed/i)
        expect(existsSync(file)).toBe(false)
      },
    })

    await chooseSettingsOption(page, `permissionMode-${QODER_MODE.DontAsk}`)
    await waitForSettingsIdle(page)
    await expectQoderModeChip(page, 'Don\'t Ask')
    // Don't Ask refuses the write without a banner, so the scenario answers no control.
    const deniedStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'dont-ask-mode-write', command)] },
      { text: 'The Dont Ask decision was recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Try the same write without asking.'))
    await modelScript.waitForSteps(deniedStep + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    expect(existsSync(file)).toBe(false)
    expect(await nativeToolResultAt(modelScript, deniedStep + 1, 'dont-ask-mode-write'))
      .toContain('the "Don\'t ask" permission mode does not prompt')
  })
})
