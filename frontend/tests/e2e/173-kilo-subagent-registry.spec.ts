import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
/**
 * 173 — KiloCode subagent transcript.
 *
 * IMPORTANT: Kilo's default model is an image model that no-ops agentic turns;
 * the kilo fixture opens the agent with an explicit text-capable model so the
 * subagent spawn actually runs.
 */
import { expect, KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo subagent registry', () => {
  kiloTest('subagent spawn creates a prompt and report transcript', async ({
    authenticatedKiloWorkspace,
    page,
  }) => {
    void authenticatedKiloWorkspace

    await expectNoRegistryRows(page)

    await sendMessage(page, 'Use your task tool to spawn a subagent that runs `echo kilo-done` and reports the result.')
    await waitForAgentIdle(page, 180_000)

    // The model may choose not to spawn; skip the spawn-dependent assertions
    // rather than fail on a real LLM's discretion.
    const row = await requireRegistryRow(kiloTest, page)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'kilo-done' })).toBeVisible()
    if (await row.getAttribute('data-status') === 'completed')
      await expect(page.getByText('Subagent reported', { exact: true })).toBeVisible()
  })
})
