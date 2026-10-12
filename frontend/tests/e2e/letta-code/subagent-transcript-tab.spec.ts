import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

lettaTest.describe('Letta Code subagents', () => {
  const CHILD_TASK = 'Count the files and report the number.'

  lettaTest('routes the prompt and report into a child tab opened from the registry row', async ({ native }) => {
    const { page, modelScript } = native
    const gate = `letta-tab-final-${crypto.randomUUID()}`
    await withCleanup(async () => {
      const childPrompt = modelScript.prompt(CHILD_TASK)
      // The rule matches the last user turn of the child. The next root request after the spawn holds the Agent call,
      // and so the child prompt, in its history. A body matcher gave the child report to that root request, and the
      // root report to the child.
      await modelScript.rule({
        name: 'the child reports its count',
        when: { user: CHILD_TASK, lastMessage: { role: 'user' } },
        respond: { gate, text: 'LETTA_CHILD_DONE' },
        once: true,
      })
      const start = await modelScript.queue(
        {
          toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-letta', {
            description: 'Count the files',
            prompt: childPrompt,
          })],
        },
        { text: 'LETTA_ROOT_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Delegate the count to a subagent, then report.'))
      await modelScript.waitForGate(gate)
      await registerLettaChildNoticeRule(native, { name: 'the Letta root handles the child completion notice', spawnCallId: 'spawn-letta', description: 'Count the files', report: 'LETTA_CHILD_DONE', reply: 'LETTA_ROOT_AFTER_CHILD_DONE', once: true })
      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps(start + 2)
      await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the child completion notice'] ?? 0).toBe(1)
      await waitForAgentIdle(page)

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
