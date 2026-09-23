import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Approvals', () => {
  // Note: The default test fixtures use approvalPolicy: "never" (bypassPermissions),
  // so approval requests won't appear. These tests verify the basic flow works
  // and can be expanded when approval-mode fixtures are added.

  codexTest('agent runs commands without approval prompts in bypass mode', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'bypass-call', 'echo "approval-test-bypass"')] },
      { text: 'The command printed approval-test-bypass.' },
    )
    await sendMessage(page, modelScript.prompt('Run: echo "approval-test-bypass"'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The command should have executed without any approval prompt.
    const chatArea = messageContents(page)
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('approval-test-bypass')
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
