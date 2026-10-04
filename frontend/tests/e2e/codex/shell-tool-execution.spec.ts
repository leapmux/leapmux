import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { readCodexExecResult } from './execResult'

codexTest.describe('codex tool execution', () => {
  codexTest('command execution shows command, output, and exit code', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // The command really runs, so the exit code the card shows is the shell's own.
    // The command text states no `codex-hello-42` and no `codex-done-55`, so only
    // the command's own output can put them in a tool row.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'exit-call', `sh -c 'echo "codex-hello-$((40 + 2))"; echo "codex-done-$((50 + 5))"; exit 7'`)] },
      { text: 'The command exited with status 7.' },
    )
    await sendMessage(page, modelScript.prompt('Run this exact command and report its result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'codex-hello-42' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'codex-done-55' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
  })
})

codexTest('returns native shell success and failure output to the following model request', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseShellToolExecution({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodexWorkspace.workspaceId, provider: AgentProvider.CODEX, readToolResult: readCodexExecResult })
})
