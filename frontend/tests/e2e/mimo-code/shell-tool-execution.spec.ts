import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createOutputGate, runWithGatedOutput } from '../helpers/outputGate'
import { bashToolCall, mimoInteractiveBashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code tool execution', () => {
  mimoTest('a shell command renders as a tool card with its output', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    // MiMo Code 0.1.15 can lose the output of a command that exits right after it
    // writes, and the tool then returns "(no output)". The gate keeps the command alive
    // until the running row shows the output (see `OutputGate`), so the output cannot be
    // lost. MiMo publishes that row from the fiber that reads the output, after the fiber
    // keeps the bytes.
    const gate = createOutputGate(createTestDirectory('mimo-shell-gate-'))
    const command = gate.hold('echo "mimo-$((40 + 2))"')
    // The command text, the prompt and the reply state no `mimo-42`, so only the
    // command's own output can put it in a tool row.
    const outputRow = () => page.locator('[data-tool-message]:visible').filter({ hasText: 'mimo-42' }).first()
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'echo-call', command)] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await runWithGatedOutput(
      { gate, shown: () => expect(outputRow(), 'the running row shows the output of the held command').toBeVisible() },
      async () => {
        await modelScript.waitForSteps()
        await waitForAgentIdle(page, 120_000)
      },
    )

    // The output reaches the card from MiMo's own metadata, not from the model's
    // reply, which does not repeat it.
    await expect(outputRow()).toBeVisible()
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
  await exerciseMiMoShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})
