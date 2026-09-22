/**
 * 111 — Cursor basic chat, against the mock endpoint.
 *
 * Cursor is the one provider that speaks neither the OpenAI nor the Anthropic
 * model API: it talks to its own backend over Connect, and its whole turn
 * travels on one bidirectional HTTP/2 stream. `helpers/cursorSurface.ts` answers
 * that backend and `helpers/cursorWire.ts` encodes it, so a scenario reaches
 * Cursor through the same `modelScript` every other provider uses.
 */
import { CURSOR_E2E_SKIP_REASON, cursorTest } from './cursor-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor Basic Chat', () => {
  cursorTest('send message and receive response', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
