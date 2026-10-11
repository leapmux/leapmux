import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, sendMessage, toggleModeWithShortcut, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('Shift+Tab toggles plan mode from the composer', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')

    await toggleModeWithShortcut(page, 'Plan')

    // The turns approve nothing, so an approval request blocks the turn and fails the test.
    const planned = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-plan', 'echo "shortcut-plan-$((40 + 2))"')] },
      { text: 'The Plan shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check whether the Plan shortcut permits a shell command.'))
    expect(await nativeToolResultAt(modelScript, planned + 1, 'shortcut-plan')).toContain('not available in Plan mode')
    await waitForAgentIdle(page)

    await toggleModeWithShortcut(page, 'Agent')

    await applyPermissionPreset(page, 'bypass')
    const executed = await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-agent', 'echo "shortcut-agent-$((40 + 2))"')] },
      { text: 'The Agent shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the shell command through the Agent shortcut.'))
    expect(await nativeToolResultAt(modelScript, executed + 1, 'shortcut-agent')).toContain('shortcut-agent-42')
    await waitForAgentIdle(page)
  })
})
