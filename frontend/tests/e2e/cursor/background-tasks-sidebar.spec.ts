import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { openCursorRunningChild } from './childScenario'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('background-tasks-sidebar: task delegation creates a registry row with a sanitized key', async ({
  authenticatedCursorWorkspace,
  page,
  modelScript,
  leapmuxServer,
}) => {
  void authenticatedCursorWorkspace

  await expectNoRegistryRows(page, leapmuxServer)

  // The remote Task service supplies this report without starting a local child.
  await modelScript.queue({
    toolCalls: [spawnSubagentToolCall(AgentProvider.CURSOR, 'spawn-cursor', {
      description: 'Ask the subagent for one word',
      prompt: 'Reply with the single word PONG.',
      report: 'PONG',
    })],
  })
  await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))
  await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)

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

cursorTest('shows the actual native child identity and final reply in its sidebar row', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const child = await openCursorRunningChild(context)
  await withCleanup(async () => {
    await expect(child.row).toHaveAttribute('data-child-agent-id', child.childId)
    await expect(child.row).toHaveAttribute('data-status', 'running')
    expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
    await expect(userBubbles(page).filter({ hasText: child.prompt }).first()).toBeVisible()
  }, child.finish)
  await expectRowBecomesFinal(page, child.row)
  await expectSectionPersists(page)
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: child.answer }).first()).toBeVisible()
})
