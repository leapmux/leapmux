import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage, userBubbles } from './helpers/ui'
/**
 * 176 — Pi subagent registry (pi-subagents extension).
 *
 * Pi's tool_execution_update details feed a LIVE activity line. Foreground: a
 * running row whose activity text changes over time. Background: after the
 * Agent tool's own result renders in the parent transcript, the row stays
 * running (background re-key by agent id) until a subagent-notification
 * message closes it. The child tab keeps the spawn prompt and final report.
 */
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi subagent registry', () => {
  piTest('foreground subagent shows a live activity row', async ({
    authenticatedPiWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedPiWorkspace

    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turns it runs on its own
    // reach this script. The rule matches text the PARENT prompt does not
    // carry, or the parent's own turn would take this answer instead of the
    // queued spawn.
    await modelScript.rule({
      name: 'the child works through its multi-step task',
      // ANCHORED. A parent turn that carries the tool request embeds this whole
      // prompt, so an unanchored pattern answers the PARENT's turn and the
      // queued step is never consumed.
      when: { user: '^List three fruits' },
      respond: { text: 'Apple, banana, cherry. One, two, three, four, five. Done.' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.PI, 'spawn-pi', {
        description: 'Run the fruit task',
        prompt: modelScript.prompt('List three fruits, then count to five, then report done.'),
      })],
    })
    // pi-subagents notifies the PARENT once the child finishes, which is a turn
    // beyond the two this test queues. Its count depends on the extension, not
    // on the test, so a rule answers it rather than a step.
    await modelScript.rule({
      name: 'the parent acknowledges the subagent notification',
      when: { user: '<task-notification>' },
      respond: { text: 'The subagent finished the counting task.' },
    })
    await modelScript.queue({ text: 'The subagent listed three fruits and counted to five.' })
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))
    await modelScript.waitForSteps(2)

    // Wait for the row directly (waitForAgentIdle now blocks while a task is
    // active, since the indicator stays up for an active task count).
    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: /list three fruits/i })).toBeVisible()
    // The report bubble carries BOTH the label and the child's answer, which is
    // the form 188 already proves. It used to read
    // `getByText('Subagent reported', { exact: true })` behind a status guard:
    // `exact` demands that the element's WHOLE text be those two words, so it
    // could never match a bubble that also carries the report, and the guard
    // kept it from ever running.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /Apple, banana, cherry/ })).toBeVisible()
  })
})
