import { expect } from '@playwright/test'

import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, controlBanner, expectSettingsChip, openPlusMenu, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The Bypass shortcut selects the provider's native permission preset. A real tool must execute without a permission banner.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp permissions', () => {
  ampTest('retains an already-selected native Bypass preset across two preparation calls', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
    const before = await currentNativeAgent(context)
    const group = before.optionGroups.find(option => option.id === 'permissionMode')
    expect(group?.mutable).toBe(true)
    expect(group?.currentValue).toBe(AMP_PERMISSION_MODE.AllowAll)
    expect(group?.options.map(option => option.id)).toEqual(expect.arrayContaining([AMP_PERMISSION_MODE.Ask, AMP_PERMISSION_MODE.AllowAll]))

    await applyPermissionPreset(page, 'bypass')
    await applyPermissionPreset(page, 'bypass')
    expect((await currentNativeAgent(context)).optionGroups.find(option => option.id === 'permissionMode')?.currentValue).toBe(AMP_PERMISSION_MODE.AllowAll)
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'active-bypass-call', 'printf "ACTIVEBYPASS%s\\n" "$((40 + 2))"')] },
      { text: 'The already-selected native preset ran the command.' },
    )
    await sendMessage(page, modelScript.prompt('Run the native arithmetic command under the selected permission preset.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = status.requests.find(record => record.stepIndex === 1)
    if (!request)
      throw new Error('The native preset test contains no actual command result request.')
    expect((await ampToolResultReader(context)(request, 'active-bypass-call')).text).toContain('ACTIVEBYPASS42')
    await expect(controlBanner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('ACTIVEBYPASS42')
  })

  ampTest('runs every call without a banner in Allow All, which the Bypass shortcut selects', async ({ askingAmpWorkspace, page, modelScript }) => {
    void askingAmpWorkspace
    // Amp has no model axis, so the mode chip is the sign that the settings arrived.
    await expectSettingsChip(page, 'Medium')

    // Amp has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Allow All, which applies to the next call at once.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.AMP, 'bypass-call', 'echo "amp-$((60 + 6))"')] },
      { text: 'The command ran without a banner.' },
    )
    await sendMessage(page, modelScript.prompt('Run the third arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(controlBanner(page)).toHaveCount(0)
    await expect.poll(() => chatText(page)).toContain('amp-66')
  })
})
