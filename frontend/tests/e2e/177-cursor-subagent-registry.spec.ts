/**
 * 177 — Cursor subagent registry (registry-only, added after the live-probe
 * upgrade).
 *
 * Cursor's Task tool surfaces a spawn tool_call with rawInput._toolName ==
 * "task" and a title "Task: <desc>". Registry-only: no child transcript. The
 * observed toolCallId can contain an embedded newline; the neutral layer
 * sanitizes the row key, so data attributes must never contain a control char.
 */
import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import {
  expectRegistryOnlySubagentEnds,
  expectRegistrySectionAbsent,
  waitForRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest.describe('Cursor subagent registry', () => {
  cursorTest('Task delegation creates a registry row with a sanitized key', async ({
    authenticatedCursorWorkspace,
    page,
  }) => {
    void authenticatedCursorWorkspace

    await expectRegistrySectionAbsent(page)

    await sendMessage(page, 'Delegate this to a subagent: reply with the single word PONG.')
    await waitForAgentIdle(page, 180_000)

    const row = await waitForRegistryRow(page)

    // Regression guard: the row's testid/data attributes must never contain a
    // control character (the embedded-newline toolCallId quirk is sanitized in
    // the neutral layer before it reaches the DOM). Built without a control-char
    // regex literal so no-control-regex stays satisfied.
    const rowHtml = await row.evaluate(el => el.outerHTML)
    const hasControlChar = Array.from(rowHtml).some(ch => ch.codePointAt(0)! < 0x20)
    expect(hasControlChar).toBe(false)

    await expectRegistryOnlySubagentEnds(page, row)
  })
})
