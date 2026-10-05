import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest.describe('codex tool execution', () => {
  codexTest('persists completed reasoning during shell command execution', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
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
    // Two separate shell calls, each with its own reasoning, then the report.
    // The reasoning is what the thought band draws. The command text and the
    // prompt state no `codex-42`, so only the second command's own output can put
    // it in a tool row. The scripted reply states it, so the reply check below
    // proves that the reply is drawn, before and after the reload.
    await modelScript.queue(
      { reasoning: 'First I check the working directory.', toolCalls: [bashToolCall(AgentProvider.CODEX, 'pwd-call', 'pwd')] },
      { reasoning: 'Now the arithmetic, in its own call.', toolCalls: [bashToolCall(AgentProvider.CODEX, 'echo-call', 'echo "codex-$((40 + 2))"')] },
      { text: 'The first call printed the working directory and the second printed codex-42.' },
    )
    await sendMessage(page, modelScript.prompt('First run pwd. Then, in a separate shell call, run the arithmetic command. Compare the two results and report both. Do not modify files.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-42' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    expect(await page.evaluate(() => (window as Window & { __codexReasoningSeen?: boolean }).__codexReasoningSeen)).toBe(true)

    await page.reload()
    await expect(page.locator('[data-band="thought"]:visible').filter({ hasText: 'Thinking' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-42' }).first()).toBeVisible()
  })
})
