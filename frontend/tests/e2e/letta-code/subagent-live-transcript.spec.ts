import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

lettaTest.describe('Letta Code subagents', () => {
  const PROVIDER = AgentProvider.LETTA

  const CHILD_READ_TASK = 'Read the marker file and report its content.'

  const CHILD_READ_MARKER = 'LETTA_CHILD_READ_MARKER'

  const CHILD_GATE = 'letta-child-final'

  lettaTest('shows the child Read result before its final model answer', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId, provider: PROVIDER }
    await withCleanup(async () => {
      const note = join(authenticatedLettaWorkspace.workingDir, 'letta-child-note.txt')
      writeFileSync(note, `${CHILD_READ_MARKER}\n`)
      const childPrompt = modelScript.prompt(`${CHILD_READ_TASK}\nPath: ${note}`)
      await modelScript.rule(LETTA_TITLE_RULE)
      // Matched on the child's own last user turn. The root's next request after the
      // spawn carries the Agent call, and so the child prompt, in its history: a body
      // matcher gave that root request the child's turn and the child the root's.
      await modelScript.rule(
        {
          name: 'the Letta child reads the marker file',
          when: { user: CHILD_READ_TASK, lastMessage: { role: 'user' } },
          respond: {
            text: 'LETTA_CHILD_EARLY',
            toolCalls: [readToolCall(PROVIDER, 'letta-child-read', note)],
          },
          once: true,
        },
        {
          name: 'the Letta child reports the marker',
          when: { user: CHILD_READ_TASK, lastMessage: { role: 'tool', text: CHILD_READ_MARKER } },
          respond: { gate: CHILD_GATE, text: 'LETTA_CHILD_FINAL' },
          once: true,
        },
      )
      await modelScript.queue(
        { toolCalls: [spawnSubagentToolCall(PROVIDER, 'letta-live-child', {
          description: 'Read the marker file',
          prompt: childPrompt,
        })] },
        { text: 'LETTA_LIVE_ROOT_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Ask the child to read the marker file, then report.'))
      await modelScript.waitForGate(CHILD_GATE)
      await registerLettaChildNoticeRule(context, { name: 'the Letta root handles the marker child completion notice', spawnCallId: 'letta-live-child', description: 'Read the marker file', report: 'LETTA_CHILD_FINAL', reply: 'LETTA_LIVE_ROOT_AFTER_CHILD_DONE', once: true })
      const rootTabID = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id')
      if (!rootTabID)
        throw new Error('The Letta root tab contains no agent ID.')
      try {
        const row = await requireRegistryRow(page)
        await expect(row).toHaveAttribute('data-status', 'running')
        const childTabID = await openChildTabFromRow(page, row)
        await expect(userBubbles(page).filter({ hasText: CHILD_READ_TASK })).toHaveCount(1)
        await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_EARLY' })).toHaveCount(1)
        await expect(messageContents(page).filter({ hasText: CHILD_READ_MARKER }).first()).toBeVisible()
        await tabById(page, rootTabID).click()
        await expect(messageContents(page).filter({ hasText: CHILD_READ_MARKER })).toHaveCount(0)
        await tabById(page, childTabID).click()
      }
      finally {
        await modelScript.releaseGateIfHeld(CHILD_GATE)
      }
      await modelScript.waitForSteps()
      await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the marker child completion notice'] ?? 0).toBe(1)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_FINAL' })).toHaveCount(1)
      const rows = await messageContents(page).allTextContents()
      const promptIndex = rows.findIndex(text => text.includes(CHILD_READ_TASK))
      const earlyIndex = rows.findIndex(text => text.includes('LETTA_CHILD_EARLY'))
      const readIndex = rows.findIndex(text => text.includes(CHILD_READ_MARKER))
      const finalIndex = rows.findIndex(text => text.includes('LETTA_CHILD_FINAL'))
      expect(promptIndex).toBeGreaterThanOrEqual(0)
      expect(earlyIndex).toBeGreaterThan(promptIndex)
      expect(readIndex).toBeGreaterThan(earlyIndex)
      expect(finalIndex).toBeGreaterThan(readIndex)
      await tabById(page, rootTabID).click()
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_LIVE_ROOT_DONE' })).toHaveCount(1)
      await expectRowBecomesFinal(page, await requireRegistryRow(page))
    }, async () => {
      await modelScript.releaseGateIfHeld(CHILD_GATE)
    })
  })
})
