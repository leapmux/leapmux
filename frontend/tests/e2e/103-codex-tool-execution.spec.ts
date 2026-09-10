import { codexTest, expect } from './codex-fixtures'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('codex tool execution', () => {
  codexTest('persists completed reasoning during shell command execution', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await page.evaluate(() => {
      const state = window as Window & { __codexReasoningSeen?: boolean }
      const reasoningVisible = () => Array.from(document.querySelectorAll<HTMLElement>('[data-band="thought"]'))
        .some((element) => {
          const style = getComputedStyle(element)
          return style.display !== 'none'
            && style.visibility !== 'hidden'
            && element.getClientRects().length > 0
            && element.textContent?.includes('Thinking')
        })
      const observer = new MutationObserver(() => {
        if (reasoningVisible()) {
          state.__codexReasoningSeen = true
          observer.disconnect()
        }
      })
      state.__codexReasoningSeen = reasoningVisible()
      observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    })
    await sendMessage(page, 'First run pwd. Then, in a separate shell call, run echo "codex-test-output". Compare the two results and report both. Do not modify files.')
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
    expect(await page.evaluate(() => (window as Window & { __codexReasoningSeen?: boolean }).__codexReasoningSeen)).toBe(true)

    await page.reload()
    await expect(page.locator('[data-band="thought"]:visible').filter({ hasText: 'Thinking' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
  })

  codexTest('command execution shows command, output, and exit code', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, `Run this exact command and report its result: sh -c 'echo "hello-from-codex"; echo "done"; exit 7'`)
    await waitForAgentIdle(page, 120_000)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'hello-from-codex' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'done' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
  })

  codexTest('file edit triggers file change rendering', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Create a file called /tmp/codex-test-file.txt with the content "codex was here"')
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: /codex-test-file|codex was here/ }).first()).toBeVisible()
  })
})
