import { codexTest, expect } from './codex-fixtures'
import { sendMessage, setInitialBrowserPref, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Turn End Sound', () => {
  codexTest('should play ding-dong sound when Codex turn ends with tool use', async ({ page, authenticatedCodexWorkspace }) => {
    void authenticatedCodexWorkspace // fixture trigger

    // Set up audio spy and preference
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await setInitialBrowserPref(page, 'turnEndSound', 'ding-dong')

    // Reload so the init scripts take effect
    await page.reload()
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // Send a message that triggers tool use (command execution) so num_tool_uses > 0
    await sendMessage(page, 'Run the command `pwd` and tell me the result.')
    await waitForAgentIdle(page, 120_000)

    // Give a short moment for the effect to fire
    await page.waitForTimeout(500)

    // Verify the doorbell sound was played
    const calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(calls.some((src: string) => src.includes('benkirb-electronic-doorbell'))).toBe(true)
  })

  codexTest('should NOT play sound for simple Codex text exchange', async ({ page, authenticatedCodexWorkspace }) => {
    void authenticatedCodexWorkspace // fixture trigger

    // Set up audio spy and preference
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await setInitialBrowserPref(page, 'turnEndSound', 'ding-dong')

    // Reload so the init scripts take effect
    await page.reload()
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // Send a simple question that completes without tool use (num_tool_uses = 0)
    await sendMessage(page, 'What is 2+2? Reply with just the number, nothing else.')
    await waitForAgentIdle(page, 120_000)

    await page.waitForTimeout(500)

    // Verify no doorbell sound was played (simple exchange suppressed)
    const calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(calls.some((src: string) => src.includes('benkirb-electronic-doorbell'))).toBe(false)
  })
})
