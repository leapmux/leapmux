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
    modelScript,
  }) => {
    void authenticatedReasonixWorkspace

    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turns it runs on its own
    // reach this script. NOT anchored: Reasonix opens a child turn with a
    // host-injected `<subagent-context event="SubagentStart">` block, so `^`
    // never matches the prompt. It needs no anchor either, because Reasonix
    // keeps a tool result out of the user turn, so no parent turn carries a
    // copy of the child's prompt.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { text: 'PONG' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.REASONIX, 'spawn-reasonix', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt('Reply with the single word PONG.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent reported PONG.' })
    await sendMessage(page, modelScript.prompt('Delegate one word to a read-only subagent.'))
    await modelScript.waitForSteps(2)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const row = await requireRegistryRow(page)
    const r = row!

    await expectRowBecomesFinal(page, r)
    await expectSectionPersists(page)
    await expect.poll(async () => await r.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, r)
    await expect(userBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()
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
