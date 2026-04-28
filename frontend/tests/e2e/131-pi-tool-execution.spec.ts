import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Coding Agent Tool Execution', () => {
  piTest('bash command execution renders output in chat', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'Run the bash command: echo "pi-test-output" and show me the output.')
    await waitForAgentIdle(page, 180_000)

    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('pi-test-output')
  })

  piTest('write tool creates a file and the chat surfaces the path', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'Use the write tool to create /tmp/pi-test-file.txt with the content "pi was here".')
    await waitForAgentIdle(page, 180_000)

    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    // Either the path or the content should appear in the rendered chat.
    expect(joined.includes('pi-test-file') || joined.includes('pi was here')).toBeTruthy()
  })
})
