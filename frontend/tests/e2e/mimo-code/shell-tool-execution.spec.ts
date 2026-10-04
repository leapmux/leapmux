import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall, mimoInteractiveBashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code tool execution', () => {
  mimoTest('a shell command renders as a tool card with its output', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    // The command text, the prompt and the reply state no `mimo-42`, so only the
    // command's own output can put it in a tool row.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'echo-call', 'echo "mimo-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The output reaches the card from MiMo's own metadata, not from the model's
    // reply, which does not repeat it.
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'mimo-42' }).first()).toBeVisible()
    const railedRows = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible')
    await expect(railedRows.first()).toBeVisible()
  })
})

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code interactive commands', () => {
  // Nobody can type into a command that LeapMux runs, so the worker refuses the
  // request at once. The turn goes on: the model reads the refusal as the
  // command's output and answers.
  mimoTest('refuses an interactive command without blocking the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue(
      { toolCalls: [mimoInteractiveBashToolCall('interactive-call', 'read -p "Name? " name; echo "hi $name"')] },
      { text: 'INTERACTIVE_REFUSED' },
    )
    await sendMessage(page, modelScript.prompt('Ask for my name in the shell.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(messageContents(page).filter({ hasText: 'LeapMux cannot run an interactive command' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'INTERACTIVE_REFUSED' }).first()).toBeVisible()
  })
})

mimoTest('preserves a literal private shell path with spaces and metacharacters', async ({ authenticatedMiMoWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})
