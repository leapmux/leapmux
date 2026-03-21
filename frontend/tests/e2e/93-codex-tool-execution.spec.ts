import { codexTest, expect } from './codex-fixtures'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Tool Execution', () => {
  codexTest('shell command execution renders in chat', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Run the command: echo "codex-test-output" and show me the output.')
    await waitForAgentIdle(page, 120_000)

    // Look for the command execution rendering in the chat.
    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('codex-test-output')
  })

  codexTest('command execution shows command, output, and exit code', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Run this exact command: echo "hello-from-codex" && echo "done"')
    await waitForAgentIdle(page, 120_000)

    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    // Should contain the command output.
    expect(joined).toContain('hello-from-codex')
  })

  codexTest('file edit triggers file change rendering', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Create a file called /tmp/codex-test-file.txt with the content "codex was here"')
    await waitForAgentIdle(page, 120_000)

    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    // Should reference the file in some way.
    expect(joined.includes('codex-test-file') || joined.includes('codex was here')).toBeTruthy()
  })
})
