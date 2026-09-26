import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectNoRegistryRows,
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 289 — Qoder CLI subagent registry.
 *
 * The `Agent` tool spawns a child agent. In Default mode the spawn raises a
 * banner first; allowing it draws a row in the Background tasks section, and
 * the row opens the child's own transcript tab. The child's model call holds
 * its task as the last user text, so a rule answers it out of order from the
 * parent's turns.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QODER

qoderTest.describe('Qoder CLI subagent registry', () => {
  qoderTest('follows one subagent from its spawn to its report, with its own transcript', async ({ askingQoderWorkspace, page, modelScript }) => {
    void askingQoderWorkspace
    await expectNoRegistryRows(page)

    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { reasoning: 'The task asks for one word.', text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-qoder', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))

    // The spawn is a delegated tool call, so Default asks first. Wait for the
    // model's answer before the banner: an agent process takes tens of seconds
    // to start, and the banner assertion's own timeout would otherwise expire
    // before the turn runs.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('Agent')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Ask for one word')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    expect((await modelScript.status()).ruleMatches['the child answers its one-word task']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' })).toBeVisible()

    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
  })
})
