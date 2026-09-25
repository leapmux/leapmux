import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

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
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-42' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    expect(await page.evaluate(() => (window as Window & { __codexReasoningSeen?: boolean }).__codexReasoningSeen)).toBe(true)

    await page.reload()
    await expect(page.locator('[data-band="thought"]:visible').filter({ hasText: 'Thinking' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-42' }).first()).toBeVisible()
  })

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

  codexTest('file edit triggers file change rendering', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const path = '/tmp/codex-test-file.txt'
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.CODEX, 'write-call', { path, content: 'codex was here' })] },
      { text: `I created ${path} with the content "codex was here".` },
    )
    await sendMessage(page, modelScript.prompt(`Create a file called ${path} with the content "codex was here"`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    // The rows that the title promises. A write that creates a file draws two:
    // - The tool row states the path and the line count. It carries no diff
    //   badge, because the line count takes the place of the badge
    //   (`renderWriteTitle`). So `fileChangeRow` never finds this row.
    // - The diff row draws the added line.
    // The prompt and the scripted reply state the same path and text. Neither is
    // a tool row or a diff, so only the write itself can pass these two checks.
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: `${path} (1 line)` }).first()).toBeVisible()
    await expect(page.locator(`[data-file-diff][data-file-path="${path}"]:visible`).filter({ hasText: 'codex was here' }).first()).toBeVisible()
    // The reply alone is scripted text, so it proves only that the reply is drawn.
    await expect(assistantBubbles(page).filter({ hasText: /codex-test-file|codex was here/ }).first()).toBeVisible()
  })
})
