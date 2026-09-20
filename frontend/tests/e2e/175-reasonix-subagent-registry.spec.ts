import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles } from './helpers/ui'
/**
 * 175 — Reasonix subagent transcript.
 *
 * Reasonix withholds ToolProgress from ACP by design, so the row appears with
 * the spawn title and stays running until the terminal tool_result. There is
 * no per-tool activity-text update to assert. The task result still supplies
 * the prompt and final report.
 */
import { expect, REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest.describe('Reasonix subagent registry', () => {
  reasonixTest('subagent spawn creates a prompt and report transcript', async ({
    authenticatedReasonixWorkspace,
    page,
  }) => {
    void authenticatedReasonixWorkspace

    await expectNoRegistryRows(page)

    await sendMessage(page, 'Use read_only_task exactly once. Give it this prompt: reply with the single word PONG. Report what it said.')

    // The model may choose not to spawn; skip the spawn-dependent assertions.
    const row = await requireRegistryRow(reasonixTest, page)
    const r = row!

    await expectRowBecomesFinal(page, r)
    await expectSectionPersists(page)
    await expect.poll(async () => await r.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, r)
    await expect(userBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()
    if (await r.getAttribute('data-status') === 'completed')
      await expect(page.getByText('Subagent reported', { exact: true })).toBeVisible()
  })
})
