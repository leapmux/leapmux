import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectCodewhalePosture } from './scenarios'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('applies the mode, the effort and the posture, and keeps them after a reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')

    await chooseSettingsOption(page, 'codewhale_mode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    const planStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'mode-plan', 'echo "mode-plan-$((40 + 2))"')] },
      { text: 'The Plan turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check whether Plan mode permits a shell command.'))
    expect(await nativeToolResultAt(modelScript, planStep + 1, 'mode-plan')).toContain('not available in Plan mode')
    await waitForAgentIdle(page)

    // Both presets map onto a posture of the runtime: Smart onto its own review
    // rules, Bypass onto full access.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    await expectCodewhalePosture(page, 'full_access')
    await applyPermissionPreset(page, 'smart')
    await expectCodewhalePosture(page, 'auto_review')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')
    await expectCodewhalePosture(page, 'auto_review')

    const restoredStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'mode-plan-restored', 'echo "restored-plan-$((40 + 2))"')] },
      { text: 'The restored Plan turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check the restored native Plan restriction.'))
    expect(await nativeToolResultAt(modelScript, restoredStep + 1, 'mode-plan-restored')).toContain('not available in Plan mode')
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, 'codewhale_mode-agent')
    await chooseSettingsOption(page, 'permissionMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')

    await applyPermissionPreset(page, 'bypass')
    const agentStep = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'mode-agent', 'echo "mode-agent-$((40 + 2))"')] },
      { text: 'The Agent turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the shell command in Agent mode.'))
    expect(await nativeToolResultAt(modelScript, agentStep + 1, 'mode-agent')).toContain('mode-agent-42')
    await waitForAgentIdle(page)
    await chooseSettingsOption(page, 'permissionMode-ask')
    await waitForSettingsIdle(page)
    await expectCodewhalePosture(page, 'ask')
  })
})
