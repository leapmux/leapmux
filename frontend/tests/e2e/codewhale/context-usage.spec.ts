import type { Page } from '@playwright/test'
import type { MockModelUsage } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { readContextRow } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

/** The window that Codewhale 0.10.0 states for the model of the E2E environment. */
const MODEL_WINDOW_TOKENS = 1_000_000

/** Run one scripted turn that reports `usage` and wait until it ends. */
async function runTurn(page: Page, modelScript: ModelScript, prompt: string, usage: MockModelUsage): Promise<void> {
  const start = (await modelScript.status()).stepCount
  await modelScript.queue({ text: 'Usage recorded.', usage })
  await sendMessage(page, modelScript.prompt(prompt))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)
}

/**
 * Codewhale 0.10.0 reports its context in GET /v1/threads/{id}/context. The report states the
 * window of the model (`window_tokens`) and the runtime's own estimate of the context
 * (`input_tokens`). The estimate counts the system prompt, the tools, and the conversation.
 * It does not follow the counts of one model request: `billed_input_tokens` follows those.
 * The Worker shows the estimate and the window in the Context row, and the counts of one
 * request never replace them (`providers/codewhale/usage.go`).
 *
 * So the row follows the conversation. A long prompt makes the row grow, although the scripted
 * request then reports far fewer tokens than the request before.
 */
codewhaleTest('reports the native context estimate and window in the agent info card', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
  void authenticatedCodewhaleWorkspace
  await runTurn(page, modelScript, 'Reply once.', { inputTokens: 12_000, outputTokens: 40 })
  // The window comes from the context report. No count of a request states it, so the window
  // shows that the report reached the card.
  await expect.poll(async () => (await readContextRow(page))?.window).toBe(MODEL_WINDOW_TOKENS)
  const first = await readContextRow(page)
  if (!first)
    throw new Error('The agent info card states no Context row after the first turn.')
  expect(first.tokens).toBeGreaterThan(0)

  const filler = Array.from({ length: 1_000 }, (_, index) => `cedar${index}`).join(' ')
  await runTurn(page, modelScript, `Reply once more after this long note: ${filler}`, { inputTokens: 100, outputTokens: 10 })
  // formatTokenCount rounds to 100 tokens, and the note holds well over 1,000.
  await expect.poll(async () => ((await readContextRow(page))?.tokens ?? 0) - first.tokens).toBeGreaterThanOrEqual(1_000)
  expect((await readContextRow(page))?.window).toBe(MODEL_WINDOW_TOKENS)
})
