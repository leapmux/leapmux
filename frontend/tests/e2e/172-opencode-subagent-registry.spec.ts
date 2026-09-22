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
    modelScript,
  }) => {
    void authenticatedOpencodeWorkspace

    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turns it runs on its own
    // reach this script rather than the ambient scenario. The rule matches text
    // the PARENT prompt does not carry, or the parent's own turn would take
    // this answer instead of the queued spawn.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      // ANCHORED. A parent turn that carries the tool request embeds this whole
      // prompt, so an unanchored pattern answers the PARENT's turn and the
      // queued step is never consumed.
      when: { user: '^Reply with the single word' },
      respond: { text: 'PONG' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.OPENCODE, 'spawn-opencode', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt('Reply with the single word PONG.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent reported PONG.' })
    await sendMessage(page, modelScript.prompt('Spawn one subagent and report what it says.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page, 180_000)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
    // The report bubble carries BOTH the label and the child's answer, which is
    // the form 188 already proves. It used to read
    // `getByText('Subagent reported', { exact: true })` behind a status guard:
    // `exact` demands that the element's WHOLE text be those two words, so it
    // could never match a bubble that also carries the report, and the guard
    // kept it from ever running.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /PONG/ })).toBeVisible()
  })
})
