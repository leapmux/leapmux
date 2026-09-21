/**
 * 177 — Cursor subagent transcript.
 *
 * Cursor's Task tool surfaces a spawn tool_call with rawInput._toolName ==
 * "task" and a title "Task: <desc>". Its local store supplies the final report
 * that ACP omits. The observed toolCallId can contain an embedded newline. The
 * neutral layer sanitizes the row key, so data attributes contain no control char.
 */
import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor subagent registry', () => {
  cursorTest('Task delegation creates a registry row with a sanitized key', async ({
    authenticatedCursorWorkspace,
    page,
  }) => {
    void authenticatedCursorWorkspace

    await expectNoRegistryRows(page)

    await sendMessage(page, 'Delegate this to a subagent: reply with the single word PONG.')
    await waitForAgentIdle(page, 180_000)

    // The model may choose not to spawn; skip the spawn-dependent assertions
    // rather than fail on a real LLM's discretion.
    const row = await requireRegistryRow(cursorTest, page)

    // Regression guard: the row's testid/data attributes must never contain a
    // control character (the embedded-newline toolCallId quirk is sanitized in
    // the neutral layer before it reaches the DOM). Built without a control-char
    // regex literal so no-control-regex stays satisfied.
    const rowHtml = await row.evaluate(el => el.outerHTML)
    const hasControlChar = Array.from(rowHtml).some(ch => ch.codePointAt(0)! < 0x20)
    expect(hasControlChar).toBe(false)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()
    if (await row.getAttribute('data-status') === 'completed')
      await expect(page.getByText('Cursor subagent reported', { exact: true })).toBeVisible()
  })
})
