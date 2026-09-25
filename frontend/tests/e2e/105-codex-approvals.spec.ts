import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Approvals', () => {
  // Note: The default test fixtures use approvalPolicy: "never" (bypassPermissions),
  // so approval requests won't appear. These tests verify the basic flow works
  // and can be expanded when approval-mode fixtures are added.

  codexTest('agent runs commands without approval prompts in bypass mode', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // The command text, the prompt and the reply state no `codex-bypass-42`, so
    // only the command's own output can put it in a tool row: the command ran
    // with no approval prompt that could stop it.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'bypass-call', 'echo "codex-bypass-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-bypass-42' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
  })

  codexTest('no control banner appears in bypass mode', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'no-approval-call', 'echo "no-approval-needed"')] },
      { text: 'The command printed no-approval-needed.' },
    )
    await sendMessage(page, modelScript.prompt('Run: echo "no-approval-needed"'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The control banner should NOT appear in bypass mode.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).not.toBeVisible()
  })

  codexTest('agent writes files without approval in bypass mode', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.CODEX, 'write-call', { path: '/tmp/codex-approval-test.txt', content: 'test' })] },
      { text: 'I created /tmp/codex-approval-test.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Create a file /tmp/codex-approval-test.txt with content "test"'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // Should have completed without approval prompt.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).not.toBeVisible()
  })
})
