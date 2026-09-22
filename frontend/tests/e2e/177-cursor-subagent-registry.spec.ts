/**
 * 177 — Cursor subagent transcript, against the mock endpoint.
 *
 * Cursor's Task tool surfaces a spawn tool_call with rawInput._toolName ==
 * "task" and a title "Task: <desc>". Its local store supplies the final report
 * that ACP omits. The observed toolCallId can contain an embedded newline. The
 * neutral layer sanitizes the row key, so data attributes contain no control char.
 *
 * `helpers/cursorWire.ts` encodes the Run-stream updates that produce that tool
 * call: `tool_call_started` and `tool_call_completed`, each carrying a whole
 * `ToolCall` whose `task_tool_call` holds `TaskArgs` and a `TaskSuccess`.
 *
 * The child transcript arrives in ONE piece here: the prompt at spawn, then the
 * report when the task ends. Cursor streams nothing in between, unlike ZCode,
 * Codex and Goose. See https://github.com/leapmux/leapmux/issues/487 -- that is
 * where a test for a streaming child transcript belongs, once someone
 * establishes whether the CLI forwards a `tool_call_delta` over ACP.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest, expect } from './cursor-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
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
    modelScript,
  }) => {
    void authenticatedCursorWorkspace

    await expectNoRegistryRows(page)

    // Cursor resolves its subagent LOCALLY -- the CLI runs no child turn
    // against the endpoint -- so the child's answer rides in the scripted call
    // as `report` rather than coming from a rule, as it does for every other
    // provider.
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CURSOR, 'spawn-cursor', {
        description: 'Ask the subagent for one word',
        prompt: 'Reply with the single word PONG.',
        report: 'PONG',
      })],
    })
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page, 180_000)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)

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
    // The child's PROMPT opens its transcript, and its REPORT closes it.
    //
    // Cursor omits the report from ACP and reads it back from its own session
    // store, and the mock writes that store over the KV channel -- see
    // `cursorSetBlob`. Before that channel existed the CLI created the database
    // and never wrote a row, so this assertion is what proves the transcript
    // reaches disk and not merely the screen.
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /PONG/ })).toBeVisible()
  })
})
