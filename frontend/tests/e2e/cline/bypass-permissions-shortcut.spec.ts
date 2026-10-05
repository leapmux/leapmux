import { readFileSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, openPlusMenu, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

/**
 * The Bypass shortcut selects the provider's native permission preset. A real tool must execute without a permission banner.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CLINE

clineTest.describe('Cline control requests', () => {
  clineTest('runs every call without a banner in Auto-approve, which the Bypass shortcut selects', async ({ askingClineWorkspace, page, modelScript }) => {
    const marker = join(askingClineWorkspace.workingDir, 'bypass.txt')
    await expectSettingsChip(page, 'Act')
    // Cline has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Auto-approve, which applies to the next call at once.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Auto-approve')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'bypass-call', `printf bypass > ${marker}`)] },
      { text: 'The command ran without a banner.' },
    )
    await sendMessage(page, modelScript.prompt('Run the bypass command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(visibleControlBanner(page)).toHaveCount(0)
    expect(readFileSync(marker, 'utf8')).toBe('bypass')
  })
})
