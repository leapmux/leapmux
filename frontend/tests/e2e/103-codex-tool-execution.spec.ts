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
    // The reasoning is what the thought band draws; the second command's output
    // is what both the tool card and the reply must carry.
    await modelScript.queue(
      { reasoning: 'First I check the working directory.', toolCalls: [bashToolCall(AgentProvider.CODEX, 'pwd-call', 'pwd')] },
      { reasoning: 'Now the echo, in its own call.', toolCalls: [bashToolCall(AgentProvider.CODEX, 'echo-call', 'echo "codex-test-output"')] },
      { text: 'The first call printed the working directory and the second printed codex-test-output.' },
    )
    await sendMessage(page, modelScript.prompt('First run pwd. Then, in a separate shell call, run echo "codex-test-output". Compare the two results and report both. Do not modify files.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    expect(await page.evaluate(() => (window as Window & { __codexReasoningSeen?: boolean }).__codexReasoningSeen)).toBe(true)

    await page.reload()
    await expect(page.locator('[data-band="thought"]:visible').filter({ hasText: 'Thinking' }).first()).toBeVisible()
    await expect(page.locator('[data-band="thought"]:visible ul > li').first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'codex-test-output' }).first()).toBeVisible()
  })

  codexTest('command execution shows command, output, and exit code', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // The command really runs, so the exit code the card shows is the shell's own.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEX, 'exit-call', `sh -c 'echo "hello-from-codex"; echo "done"; exit 7'`)] },
      { text: 'The command exited with status 7.' },
    )
    await sendMessage(page, modelScript.prompt('Run this exact command and report its result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'hello-from-codex' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'done' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
  })

  codexTest('file edit triggers file change rendering', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.CODEX, 'write-call', { path: '/tmp/codex-test-file.txt', content: 'codex was here' })] },
      { text: 'I created /tmp/codex-test-file.txt with the content "codex was here".' },
    )
    await sendMessage(page, modelScript.prompt('Create a file called /tmp/codex-test-file.txt with the content "codex was here"'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: /codex-test-file|codex was here/ }).first()).toBeVisible()
  })
})
