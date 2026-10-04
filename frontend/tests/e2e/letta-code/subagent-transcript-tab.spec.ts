import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

lettaTest.describe('Letta Code subagents', () => {
  lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.LETTA

  const CHILD_TASK = 'Count the files and report the number.'

  lettaTest('routes the prompt and report into a child tab opened from the registry row', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId, provider: PROVIDER }
    const gate = `letta-tab-final-${crypto.randomUUID()}`
    await withCleanup(async () => {
      await modelScript.rule(LETTA_TITLE_RULE)
      const childPrompt = modelScript.prompt(CHILD_TASK)
      await modelScript.rule({
        name: 'the child reports its count',
        when: { body: CHILD_TASK },
        respond: { gate, text: 'LETTA_CHILD_DONE' },
        once: true,
      })
      await modelScript.queue(
        {
          toolCalls: [spawnSubagentToolCall(PROVIDER, 'spawn-letta', {
            description: 'Count the files',
            prompt: childPrompt,
          })],
        },
        { text: 'LETTA_ROOT_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Delegate the count to a subagent, then report.'))
      await modelScript.waitForGate(gate)
      await registerLettaChildNoticeRule(context, { name: 'the Letta root handles the child completion notice', spawnCallId: 'spawn-letta', description: 'Count the files', report: 'LETTA_CHILD_DONE', reply: 'LETTA_ROOT_AFTER_CHILD_DONE', once: true })
      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps()
      await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the child completion notice'] ?? 0).toBe(1)
      await waitForAgentIdle(page, 180_000)

      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_ROOT_DONE' })).not.toHaveCount(0)

      const row = await requireRegistryRow(page)
      await expectRowBecomesFinal(page, row)
      await expect(row).toContainText('Count the files')
      await openChildTabFromRow(page, row)

      // The child tab shows its native prompt and its own report.
      await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_DONE' })).toHaveCount(1)
    }, async () => {
      await modelScript.releaseGateIfHeld(gate)
    })
  })
})
