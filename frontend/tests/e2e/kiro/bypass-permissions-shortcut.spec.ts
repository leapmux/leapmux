import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, closeComposerMenus, expectSettingsOptionChosen, openPlusMenu, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro settings', () => {
  // Kiro has no preset between its own rules and every call, so Smart has no match.
  // Bypass states the allow-all preset, which Kiro reads when the session opens
  // again, and a write then runs without a request.
  kiroTest('bypass runs a write without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-ask`)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await closeComposerMenus(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-${KIRO_POLICY_PRESET.AllowAll}`)

    const written = join(workingDir, 'bypass.txt')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.KIRO, 'kiro-bypass', `printf bypass > ${written}`)] },
      { text: 'WROTE_WITHOUT_ASKING' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    expect(existsSync(written)).toBe(true)
    await expect(assistantBubbles(page).filter({ hasText: 'WROTE_WITHOUT_ASKING' })).toBeVisible()
  })
})
