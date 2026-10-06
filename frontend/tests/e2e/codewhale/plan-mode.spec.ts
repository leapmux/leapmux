import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('Shift+Tab toggles plan mode from the composer', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-plan', 'echo "shortcut-plan-$((40 + 2))"')] },
      { text: 'The Plan shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check whether the Plan shortcut permits a shell command.'))
    expect(await nativeToolResultAt(modelScript, 1, 'shortcut-plan')).toContain('not available in Plan mode')
    await waitForAgentIdle(page)

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')

    await applyPermissionPreset(page, 'bypass')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-agent', 'echo "shortcut-agent-$((40 + 2))"')] },
      { text: 'The Agent shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the shell command through the Agent shortcut.'))
    expect(await nativeToolResultAt(modelScript, 3, 'shortcut-agent')).toContain('shortcut-agent-42')
    await waitForAgentIdle(page)
  })
})
