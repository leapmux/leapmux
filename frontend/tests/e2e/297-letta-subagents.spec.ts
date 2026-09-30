import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.LETTA

/**
 * The task the child performs. A rule on the child's own user turn answers the
 * child alone, because the parent's requests never carry these words as their
 * last user turn.
 */
const CHILD_TASK = 'Count the files and report the number.'
const CHILD_READ_TASK = 'Read the marker file and report its content.'
const CHILD_READ_MARKER = 'LETTA_CHILD_READ_MARKER'
const CHILD_GATE = 'letta-child-final'

lettaTest.describe('Letta Code subagents', () => {
  // Letta's `Agent` tool spawns a subagent and opens a registry row. The row is
  // clickable and opens the child's transcript in its own tab. The child runs
  // its own turn, which the rule answers off-order.
  lettaTest('routes the prompt and report into a child tab opened from the registry row', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    const childPrompt = modelScript.prompt(CHILD_TASK)
    await modelScript.rule({
      name: 'the child reports its count',
      when: { body: CHILD_TASK },
      respond: { text: 'LETTA_CHILD_DONE' },
      once: true,
    })
    await modelScript.rule({
      name: 'the Letta root handles the child completion notice',
      when: { user: '<task-notification>' },
      respond: { text: 'LETTA_ROOT_AFTER_CHILD_DONE' },
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
    await modelScript.waitForSteps()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the child completion notice'] ?? 0).toBe(1)
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect(row).toContainText('Count the files')
    await openChildTabFromRow(page, row)

    // The child tab draws the prompt the agent gave the child and the child's
    // own report.
    await expect(userBubbles(page).filter({ hasText: CHILD_TASK })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_CHILD_DONE' })).toHaveCount(1)
  })

  lettaTest('shows the child Read result before its final model answer', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    const note = join(authenticatedLettaWorkspace.workingDir, 'letta-child-note.txt')
    writeFileSync(note, `${CHILD_READ_MARKER}\n`)
    const childPrompt = modelScript.prompt(`${CHILD_READ_TASK}\nPath: ${note}`)
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.rule(
      {
        name: 'the Letta child reads the marker file',
        when: { body: CHILD_READ_TASK },
        respond: {
          text: 'LETTA_CHILD_EARLY',
          toolCalls: [readToolCall(PROVIDER, 'letta-child-read', note)],
        },
        once: true,
      },
      {
        name: 'the Letta child reports the marker',
        when: { body: CHILD_READ_MARKER },
        respond: { gate: CHILD_GATE, text: 'LETTA_CHILD_FINAL' },
        once: true,
      },
    )
    await modelScript.rule({
      name: 'the Letta root handles the marker child completion notice',
      when: { user: '<task-notification>' },
      respond: { text: 'LETTA_LIVE_ROOT_AFTER_CHILD_DONE' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'letta-live-child', {
        description: 'Read the marker file',
        prompt: childPrompt,
      })] },
      { text: 'LETTA_LIVE_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Ask the child to read the marker file, then report.'))
    await modelScript.waitForGate(CHILD_GATE)
    const rootTabID = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id')
    if (!rootTabID)
      throw new Error('the Letta root tab has no agent id')
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
      await modelScript.releaseGate(CHILD_GATE)
    }
    await modelScript.waitForSteps()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the Letta root handles the marker child completion notice'] ?? 0).toBe(1)
    await waitForAgentIdle(page, 180_000)
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
  })
})
