import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { nativeAgentsByIds } from '../helpers/nativeScenario'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRegistryRow, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, bandRows, sendMessage, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'

/**
 * An actual native child opens its own transcript tab from the registry row. The tab must show the child's prompt and report.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline omits the child ID from streamed spawn_agent output. One active spawn lets the Worker route that output. Concurrent spawns require the completed native child session store.
 */
clineTest.describe('Cline subagent registry', () => {
  clineTest('follows one subagent from its spawn to its report, with its own transcript', async ({ authenticatedClineWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedClineWorkspace
    await expectNoRegistryRows(page, leapmuxServer)

    // The child's model call holds its task as the last user text, and the parent's
    // calls never do. A rule rather than a step: the child's call has no order the
    // test controls against the parent's.
    await modelScript.rule({
      name: 'the child answers its one-word task',
      when: { user: 'Reply with the single word PONG' },
      respond: { reasoning: 'The task asks for one word.', text: 'PONG' },
    })
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(AgentProvider.CLINE, 'spawn-cline', {
          description: 'Ask for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        })],
      },
      { text: 'The subagent reported PONG.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate one word to a subagent.'))

    // The test scripts the call, so a missing row is a failure and not the model's choice.
    const row = await requireRegistryRow(page)
    await expect(row).toContainText('Ask for one word')
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectRowBecomesFinal(page, row)
    await expect(row).toHaveAttribute('data-status', 'completed')
    await expectSectionPersists(page)
    expect((await modelScript.status()).ruleMatches['the child answers its one-word task']).toBe(1)
    await expect(assistantBubbles(page).filter({ hasText: 'The subagent reported PONG.' })).toBeVisible()
    // The child's rows stay out of the parent's transcript. A check for the word PONG
    // cannot show that, because the parent's own answer holds it. Only the child's
    // model call states reasoning, so a thought band here is a child row in the wrong
    // transcript. The child tab below holds that band, which proves that it renders.
    await expect(bandRows(page, 'thought')).toHaveCount(0)

    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PONG' }).first()).toBeVisible()
    await expect(bandRows(page, 'thought').first()).toBeVisible()
  })

  clineTest('fills the transcript of each parallel subagent from the session that Cline stores', async ({ authenticatedClineWorkspace, page, modelScript, leapmuxServer }) => {
    void authenticatedClineWorkspace
    const parentTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    expect(parentTabId).not.toBe('')
    await expectNoRegistryRows(page, leapmuxServer)

    await modelScript.rule(
      { name: 'the first child answers', when: { user: 'Reply with the single word ALPHA' }, respond: { text: 'ALPHA' } },
      { name: 'the second child answers', when: { user: 'Reply with the single word BRAVO' }, respond: { text: 'BRAVO' } },
    )
    // Both spawns in one model answer run in parallel.
    await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(AgentProvider.CLINE, 'spawn-alpha', {
            description: 'Answer alpha',
            prompt: modelScript.prompt('Reply with the single word ALPHA.'),
          }),
          spawnSubagentToolCall(AgentProvider.CLINE, 'spawn-bravo', {
            description: 'Answer bravo',
            prompt: modelScript.prompt('Reply with the single word BRAVO.'),
          }),
        ],
      },
      { text: 'Both subagents reported.' },
    )
    await sendMessage(page, modelScript.prompt('Delegate two words to two subagents.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const alpha = await expectRegistryRow(page, { titleContains: 'Answer alpha' })
    const bravo = await expectRegistryRow(page, { titleContains: 'Answer bravo' })
    for (const row of [alpha, bravo]) {
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'completed')
    }
    const status = await modelScript.status()
    expect(status.ruleMatches['the first child answers']).toBe(1)
    expect(status.ruleMatches['the second child answers']).toBe(1)

    // Each child tab holds its own task and its own answer, and never the other's. A
    // swap of the two transcripts, or of the two rows, fails one of the pairs.
    const childTabIds: string[] = []
    for (const [row, own, other] of [[alpha, 'ALPHA', 'BRAVO'], [bravo, 'BRAVO', 'ALPHA']] as const) {
      await tabById(page, parentTabId).click()
      await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
      const childTabId = await openChildTabFromRow(page, row)
      childTabIds.push(childTabId)
      await expect(userBubbles(page).filter({ hasText: `Reply with the single word ${own}` }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: own }).first()).toBeVisible()
      await expect(userBubbles(page).filter({ hasText: `Reply with the single word ${other}` })).toHaveCount(0)
      await expect(assistantBubbles(page).filter({ hasText: other })).toHaveCount(0)
    }
    expect(new Set(childTabIds).size, 'two children, not one twice').toBe(2)

    // Worker-backed: each child states the lead as its parent.
    await expect.poll(async () => {
      const agents = await nativeAgentsByIds({ leapmuxServer }, childTabIds)
      return childTabIds.map(id => agents.find(agent => agent.id === id)?.parentAgentId ?? null)
    }).toEqual([parentTabId, parentTabId])
  })
})
