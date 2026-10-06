import { expect } from '@playwright/test'

import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { ampTest } from '../amp-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup, nativeOptionValue, nativeTextStep, nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, expectNoControlBanner, expectPermissionShortcuts, expectSettingsChip, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The Bypass shortcut selects the provider's native permission preset. A real tool must execute without a permission banner.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp permissions', () => {
  ampTest('retains an already-selected native Bypass preset across two preparation calls', async ({ native }) => {
    const { page, modelScript } = native
    const before = await currentNativeAgent(native)
    const group = nativeOptionGroup(before, 'permissionMode')
    expect(group?.mutable).toBe(true)
    expect(nativeOptionValue(before, 'permissionMode')).toBe(AMP_PERMISSION_MODE.AllowAll)
    expect(group?.options.map(option => option.id)).toEqual(expect.arrayContaining([AMP_PERMISSION_MODE.Ask, AMP_PERMISSION_MODE.AllowAll]))

    await applyPermissionPreset(page, 'bypass')
    await applyPermissionPreset(page, 'bypass')
    await expectNativeOptionValue(native, 'permissionMode', AMP_PERMISSION_MODE.AllowAll)
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(native.provider, 'active-bypass-call', 'printf "ACTIVEBYPASS%s\\n" "$((40 + 2))"')] },
      nativeTextStep(native, 'The already-selected native preset ran the command.'),
    )
    await sendMessage(page, modelScript.prompt('Run the native arithmetic command under the selected permission preset.'))
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    // The Amp context reads the result through the thread of the agent, which holds the native call ID.
    expect((await nativeToolOutcome(native, await modelScript.requestAt(start + 1), 'active-bypass-call')).text).toContain('ACTIVEBYPASS42')
    await expectNoControlBanner(page)
    await expect.poll(() => chatText(page)).toContain('ACTIVEBYPASS42')
  })

  ampTest('runs every call without a banner in Allow All, which the Bypass shortcut selects', async ({ askingAmpWorkspace, page, modelScript, leapmuxServer }) => {
    // Amp has no model axis, so the mode chip is the sign that the settings arrived.
    await expectSettingsChip(page, 'Medium')

    // Amp has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Allow All, which applies to the next call at once.
    await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingAmpWorkspace.workspaceId })
    await exerciseBypassPermissions(context, {
      settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(AMP_PERMISSION_MODE.AllowAll),
    })
  })
})
