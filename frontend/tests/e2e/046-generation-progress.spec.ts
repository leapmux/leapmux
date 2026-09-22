import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

const OUTPUT_MARKER = 'LEAPMUX OUTPUT two'
const INTERRUPTION_MARKER = 'Text truncated by interruption.'

codexTest.describe('generation progress', () => {
  codexTest('shows process bytes without rendering partial process output', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await page.evaluate((marker) => {
      const state = window as Window & {
        __generationOutputSeen?: boolean
        __generationPartialOutputSeen?: boolean
        __generationBytesAtRender?: string
      }
      state.__generationOutputSeen = false
      state.__generationPartialOutputSeen = false
      const inspect = () => {
        const indicator = document.querySelector<HTMLElement>('[data-testid="thinking-indicator"]')
        const bytes = indicator?.textContent?.match(/≥?\d+(?:\.\d+)?\s+(?:B|KB|MB)/)?.[0]
        if (bytes === undefined)
          return
        state.__generationOutputSeen = true
        const rendered = Array.from(document.querySelectorAll<HTMLElement>('[data-tool-message]'))
          .some(row => row.textContent?.includes(marker))
        if (!rendered)
          return
        // A render is PARTIAL when the process kept producing after it. The
        // byte counter is the evidence: it stands still once the process ends,
        // so a reading that MOVES after the content is on screen means the
        // transcript showed output the command had not finished writing.
        //
        // Comparing the counter rather than looking for the command's last word
        // is what makes this exact: a tool row collapses long output, so the
        // last word need never appear in it, and the completed result then reads
        // as a partial one on every run.
        state.__generationBytesAtRender ??= bytes
        if (state.__generationBytesAtRender !== bytes)
          state.__generationPartialOutputSeen = true
      }
      const observer = new MutationObserver(inspect)
      observer.observe(document.body, { childList: true, subtree: true, characterData: true })
      inspect()
    }, OUTPUT_MARKER)

    // The COMMAND is real -- the bytes this asserts on are its own output,
    // produced slowly enough to observe. Only the decision to run it is scripted.
    const command = 'for word in one two three four five six seven eight; do python3 -c \'import math; math.factorial(300000)\'; echo LEAPMUX OUTPUT $word; done'
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CODEX, 'slow-loop', command)] })
    await modelScript.queue({ text: 'The command finished.' })
    await sendMessage(page, modelScript.prompt('Run the slow loop once and report that it finished.'))

    await expect.poll(async () => page.evaluate(() =>
      (window as Window & { __generationOutputSeen?: boolean }).__generationOutputSeen)).toBe(true)
    expect(await page.evaluate(() =>
      (window as Window & { __generationPartialOutputSeen?: boolean }).__generationPartialOutputSeen), 'an early word of the process output rendered while later words were still to come').toBe(false)

    await waitForAgentIdle(page, 120_000)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
  })

  codexTest('keeps interrupted model text and its marker after reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    // STREAMED, not merely delayed. This test interrupts a turn mid-answer and
    // then asserts that the partial text survived, so the answer has to be
    // arriving while the interrupt lands: a step delivered in one piece leaves
    // nothing to truncate, and the token counter the assertion below reads never
    // moves. 240 pieces at 250 ms is a minute of answer, far longer than the
    // interrupt needs.
    const essay = 'Sorting algorithms compare, partition, and merge, and each choice costs time or space. '.repeat(30)
    await modelScript.queue({ text: essay, stream: { chunkChars: 10, delayMs: 250 } })
    modelScript.allowUnconsumed('the interrupt ends the turn before the streamed answer completes')
    await sendMessage(page, modelScript.prompt('Write a detailed explanation of sorting algorithms. Use no tools and start the answer immediately.'))

    const indicator = page.locator('[data-testid="thinking-indicator"]:visible')
    await expect(indicator).toContainText('tokens')
    await page.locator('[data-testid="interrupt-button"]:visible').click()
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
  })
})
