import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
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
    modelScript,
  }) => {
    void authenticatedKiloWorkspace

    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turns it runs on its own
    // reach this script rather than the ambient scenario.
    await modelScript.rule({
      name: 'the child reports the shell result',
      when: { user: 'echo kilo-done' },
      respond: { text: 'The command printed kilo-done.' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.KILO, 'spawn-kilo', {
        description: 'Run the shell probe',
        prompt: modelScript.prompt('Run `echo kilo-done` and report the result.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent reported kilo-done.' })
    await sendMessage(page, modelScript.prompt('Spawn a subagent that runs the shell probe and reports the result.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page, 180_000)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'kilo-done' })).toBeVisible()
    // The report bubble carries BOTH the label and the child's answer, which is
    // the form 188 already proves. It used to read
    // `getByText('Subagent reported', { exact: true })` behind a status guard:
    // `exact` demands that the element's WHOLE text be those two words, so it
    // could never match a bubble that also carries the report, and the guard
    // kept it from ever running.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /kilo-done/ })).toBeVisible()
  })
})
