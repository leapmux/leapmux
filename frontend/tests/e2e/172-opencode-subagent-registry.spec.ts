import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles } from './helpers/ui'
/**
 * 172 — OpenCode subagent transcript.
 *
 * OpenCode's Agent Client Protocol bridge omits the child event stream. The
 * task result still supplies the prompt, child session id, and final report.
 */
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode subagent registry', () => {
  opencodeTest('subagent spawn creates a prompt and report transcript', async ({
    authenticatedOpencodeWorkspace,
    page,
  }) => {
    void authenticatedOpencodeWorkspace

    await expectNoRegistryRows(page)

    await sendMessage(page, 'Use your task tool to spawn one subagent whose prompt is: reply with the single word PONG. Report what it said.')

    // Registry row appears (subagent, running while it works). The model may
    // choose not to spawn; in that case skip the spawn-dependent assertions.
    const row = await requireRegistryRow(opencodeTest, page)
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
