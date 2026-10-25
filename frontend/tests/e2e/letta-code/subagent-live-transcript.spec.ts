import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { installGenerationObservation, parseGenerationCounters, waitForIncreasingCounter } from '../helpers/generationProgress'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

lettaTest.describe('Letta Code subagents', () => {
  const CHILD_READ_TASK = 'Read the marker file and report its content.'

  const CHILD_READ_MARKER = 'LETTA_CHILD_READ_MARKER'

  const CHILD_GATE = 'letta-child-final'
  const FIRST_PROGRESS_GATE = 'letta-child-progress-first'
  const SECOND_PROGRESS_GATE = 'letta-child-progress-second'

  lettaTest('shows the child Read result before its final model answer', async ({ native, authenticatedLettaWorkspace }) => {
    const { page, modelScript } = native
    await withCleanup(async () => {
      const rootTabId = await selectedAgentTabId(page)
      const note = join(authenticatedLettaWorkspace.workingDir, 'letta-child-note.txt')
      writeFileSync(note, `${CHILD_READ_MARKER}\n`)
      const childPrompt = modelScript.prompt(`${CHILD_READ_TASK}\nPath: ${note}`)
      // The rules match the last user turn of the child. The next root request after the spawn holds the Agent call,
      // and so the child prompt, in its history. A body matcher gave the child turn to that root request, and the
      // root turn to the child.
      await modelScript.rule(
        {
          name: 'the Letta child reads the marker file',
          when: { user: CHILD_READ_TASK, lastMessage: { role: 'user' } },
          respond: {
            text: 'LETTA_CHILD_EARLY',
            toolCalls: [readToolCall(native.provider, 'letta-child-read', note)],
          },
          once: true,
        },
        {
          name: 'the Letta child reports the marker',
          when: { user: CHILD_READ_TASK, lastMessage: { role: 'tool', text: CHILD_READ_MARKER } },
          respond: { gate: CHILD_GATE, text: 'LETTA_CHILD_FINAL', stream: { chunkChars: 4, delayMs: 0, gates: [{ afterChunk: 1, name: FIRST_PROGRESS_GATE }, { afterChunk: 2, name: SECOND_PROGRESS_GATE }] } },
          once: true,
        },
      )
      const start = await modelScript.queue(
        { toolCalls: [spawnSubagentToolCall(native.provider, 'letta-live-child', {
          description: 'Read the marker file',
          prompt: childPrompt,
        })] },
        { text: 'LETTA_LIVE_ROOT_DONE' },
      )
      await sendMessage(page, modelScript.prompt('Ask the child to read the marker file, then report.'))
      await modelScript.waitForGate(CHILD_GATE)
      await registerLettaChildNoticeRule(native, { name: 'the Letta root handles the marker child completion notice', spawnCallId: 'letta-live-child', description: 'Read the marker file', report: 'LETTA_CHILD_FINAL', reply: 'LETTA_LIVE_ROOT_AFTER_CHILD_DONE', once: true })
      const row = await requireRegistryRow(page)
      await expect(row).toHaveAttribute('data-status', 'running')
      const childTabId = await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: CHILD_READ_TASK })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_EARLY' })).toHaveCount(1)
      await expect(messageContents(page).filter({ hasText: CHILD_READ_MARKER }).first()).toBeVisible()
      await tabById(page, rootTabId).click()
      await expect(messageContents(page).filter({ hasText: CHILD_READ_MARKER })).toHaveCount(0)
      await tabById(page, childTabId).click()
      await page.evaluate(installGenerationObservation)
      await modelScript.releaseGate(CHILD_GATE)
      await modelScript.waitForGate(FIRST_PROGRESS_GATE)
      const first = await waitForIncreasingCounter(native, 'tokens')
      await tabById(page, rootTabId).click()
      await expect.poll(async () => {
        const indicators = await page.locator('[data-testid="thinking-indicator"]:visible').allTextContents()
        return indicators.reduce((tokens, text) => tokens + (parseGenerationCounters(text).tokens ?? 0), 0)
      }, 'child model tokens stay out of the root indicator').toBe(0)
      await tabById(page, childTabId).click()
      await page.evaluate(installGenerationObservation)
      await modelScript.releaseGate(FIRST_PROGRESS_GATE)
      await modelScript.waitForGate(SECOND_PROGRESS_GATE)
      await waitForIncreasingCounter(native, 'tokens', first)
      await modelScript.releaseGate(SECOND_PROGRESS_GATE)
      await modelScript.waitForSteps(start + 2)
      await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the marker child completion notice'] ?? 0).toBe(1)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_FINAL' })).toHaveCount(1)
      await expectRowsInOrder(messageContents(page), [CHILD_READ_TASK, 'LETTA_CHILD_EARLY', CHILD_READ_MARKER, 'LETTA_CHILD_FINAL'])
      await tabById(page, rootTabId).click()
      await expect(assistantBubbles(page).filter({ hasText: 'LETTA_LIVE_ROOT_DONE' })).toHaveCount(1)
      await expectRowBecomesFinal(page, await requireRegistryRow(page))
    }, () => finishCleanup([
      modelScript.releaseGateIfHeld(CHILD_GATE),
      modelScript.releaseGateIfHeld(FIRST_PROGRESS_GATE),
      modelScript.releaseGateIfHeld(SECOND_PROGRESS_GATE),
      page.evaluate(() => window.__nativeGenerationProbe?.stop()),
    ]))
  })
})
