import { expect } from '@playwright/test'
/** Test child tab identity and isolated transcripts. Preserve the completed span edge cases. */
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { test } from '../fixtures'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { backgroundTasksSection, expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, listAgents, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { ASSISTANT_BUBBLE_SELECTOR, sendMessage, waitForAgentIdle } from '../helpers/ui'

test.describe('Claude subagent background tasks', () => {
  test('subagent spawn creates a registry row, a child tab, and isolates the transcript', async ({
    authenticatedWorkspace,
    page,
    leapmuxServer,
    modelScript,
  }) => {
    void authenticatedWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition: no registry section yet.
    await expectNoRegistryRows(page, leapmuxServer)

    // 2. Spawn a subagent.
    //
    // The subagent's job is to WRITE something, with no shell in it anywhere.
    // Asking it to `echo` a marker gave the model a one-line shortcut it took
    // every time -- it ran the echo itself as a background Bash, produced a
    // SHELL row instead of a subagent one, and the spec skipped on every run
    // while covering nothing. A task Bash cannot do removes the shortcut. The
    // prompt is directive about the TOOL as well, since the outcome alone did
    // not imply it.
    const MARKER = 'SUBAGENT-MARKER-1'
    // The CHILD's prompt carries the marker too, so the turns it runs on its own
    // reach this script rather than the ambient scenario.
    await modelScript.fallback({ text: `The subagent wrote about the ocean and ended with ${MARKER}.` })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'spawn-ocean', {
        description: 'Write about the ocean',
        prompt: modelScript.prompt(`Write two sentences about the ocean, then end your reply with the token ${MARKER}.`),
      })],
    })
    await sendMessage(page, modelScript.prompt('Spawn one general-purpose subagent to write about the ocean, then tell me what it wrote.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // 3. Sidebar: section + a subagent row. The spawn is scripted, so a
    //    missing row is a defect, and requireRegistryRow fails on it.
    const row = await requireRegistryRow(page)
    await expect(backgroundTasksSection(page)).toBeVisible()

    // A clickable row is a <button>, which Oat's base button rule renders at
    // var(--font-medium). The row must override that and stay at the normal
    // weight, so a subagent does not read as emphasized against the shell rows.
    // Only a real browser resolves the cascade, so no unit test can see this.
    await expect(row).toHaveCSS('font-weight', '400')

    // 4. Wait for the row to link to a child transcript (EnsureChildAgent runs
    //    at task_started; the child-agent-id propagates via the next broadcast).
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')

    // 5. Open the tab from the sidebar row. The helper asserts the agent-tab
    //    count grew by exactly one and returns the new tab's id.
    const childTabId = await openChildTabFromRow(page, row)

    // 6. Worker-backed: the child agent exists with parent linkage + a
    //    non-empty spawn span id. Query the worker directly for that tab id.
    let child: { id: string, parentAgentId: string, spawnSpanId: string } | null = null
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [childTabId])
      if (!agents)
        return null
      const found = agents.find(a => a.id === childTabId)
      child = found ? { id: found.id, parentAgentId: found.parentAgentId, spawnSpanId: found.spawnSpanId } : null
      return child
    }).not.toBeNull()
    expect(child!.parentAgentId).not.toBe('')
    expect(child!.spawnSpanId).not.toBe('')

    // 8. Completion: the row reaches a final status; the section persists.
    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)

    // 9. The registry's kind tabs. A subagent row lives under Subagents and not
    //    under Shell, and All shows it again. Only a real spawn puts a row in
    //    the section at all, which is why this rides on this test rather than
    //    standing alone.
    const kindTab = (key: string) => page.locator(`[data-testid="bg-task-filter-${key}"]:visible`)
    await expect(kindTab('all')).toHaveAttribute('aria-selected', 'true')
    await kindTab('subagent').click()
    await expect(row).toBeVisible()
    await kindTab('shell').click()
    await expect(row).not.toBeVisible()
    await kindTab('all').click()
    await expect(row).toBeVisible()

    // 10. Closing the parent tab takes its subagent tab with it. The child is a
    //     transcript the parent's process feeds, so left behind it is a tab
    //     nothing can add to. The worktree prompt is the parent's own and may or
    //     may not appear here (it depends on the working dir's git state), so
    //     answer it when it does.
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    const parentTab = page.locator(
      `[data-testid="tab"][data-tab-type="agent"]:not([data-tab-id="${childTabId}"])`,
    ).first()
    await parentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Wait for whichever the inspect produces -- the prompt, or the close going
    // straight through -- rather than reading visibility before either lands.
    const closeDialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Close Last Tab' }) })
    await expect.poll(async () =>
      await closeDialog.count() > 0 || await agentTabs.count() === 0,
    ).toBe(true)
    if (await closeDialog.count() > 0) {
      await closeDialog.getByRole('button', { name: 'Close anyway' }).click()
      await closeDialog.getByRole('button', { name: 'Confirm?' }).click()
    }

    await expect(page.locator(`[data-testid="tab"][data-tab-id="${childTabId}"]`)).toHaveCount(0)
    await expect(agentTabs).toHaveCount(0)
  })
})

/**
 * The Claude Agent tool's result header.
 *
 * The middle part is the TASK title when the payload carries one -- a launch
 * does, quoted and with spaces in it -- and the agent id when it does not, which
 * is the fallback when neither the result nor the paired tool_use input supplies
 * a description. The alternation covers both while keeping each side anchored.
 *
 * A synchronous result now receives its description from the paired tool_use
 * input, so the quoted form appears for synchronous runs as well.
 *
 * NOT `.+?`, which this pattern used and which fails in both directions: `.`
 * matches a space, so it crosses out of the header into ordinary assistant prose
 * ("I'll launch the Agent tool and report once it completed"), and that row
 * sits EARLIER in the DOM, so `.first()` picks a plain text row whose
 * data-span-columns is trivially 0 -- the assertion below then passes however
 * the spawn card renders. `.` also does not match a newline, so a model-written
 * title with a line break made the locator find nothing.
 *
 * Inside the quotes: `[\s\S]*?`, LAZY, and not `[^"]*`. The title is model
 * prose, so a double quote in it is possible -- and `[^"]*` stops dead at that
 * quote, then demands a status where the next character sits, so the whole
 * locator matched nothing and the assertion failed red against a card that
 * rendered correctly. `[\s\S]` spans a newline, and the lazy quantifier stops at
 * the FIRST quote that a status follows rather than running to the last quote on
 * the page. `\S+` keeps the bare-id form from crossing a space.
 */
const AGENT_RESULT_HEADER = /Agent (?:"[\s\S]*?"|\S+) (?:completed|failed|launched asynchronously|launched remotely)/

/** The Agent tool's own card title, which carries the subagent type. */
const AGENT_TYPE = 'general-purpose'

test.describe('subagent spawn has no span', () => {
  test('the spawn rows draw no rail of their own', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace

    // The spawn is the one tool this turn runs, which is what makes the rail
    // count below unambiguous: a column the spawn rows DID draw could otherwise
    // belong to some other tool the model chose to run beside it.
    const MARKER = 'SPAN-SPAWN-MARKER'
    // How many turns a CHILD runs is the provider's business, not this test's:
    // it summarises, it reports, and each of those is a request the queue never
    // planned for. The fallback answers them so an unplanned turn does not fail
    // a test whose subject is the rail geometry of two rows.
    await modelScript.fallback({ text: `The subagent wrote about the tide and ended with ${MARKER}.` })
    await modelScript.rule({
      name: 'the child writes its sentence',
      when: { user: 'one sentence about the tide' },
      respond: { text: `The tide turns twice a day. ${MARKER}` },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'spawn-tide', {
        description: 'Write about the tide',
        prompt: modelScript.prompt(`Write one sentence about the tide, then end your reply with the token ${MARKER}.`),
      })],
    })
    await modelScript.queue({ text: `The subagent wrote about the tide and ended with ${MARKER}.` })
    await sendMessage(page, modelScript.prompt('Spawn one general-purpose subagent to write about the tide, then tell me what it wrote.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)

    // Skips when the model declined to spawn.
    await requireRegistryRow(page)

    // Rows are scoped to :visible — ChatView renders every unmeasured row twice
    // and the sidebar is mounted twice, so an unscoped locator picks the wrong
    // copy.
    const spawnResultRow = page
      .locator('[data-span-columns]:visible')
      .filter({ hasText: AGENT_RESULT_HEADER })
      .first()
    await expect(spawnResultRow).toBeVisible()

    // The spawn's own result draws no rail. Any column it DOES show would have
    // to come from another tool that is still running, and this prompt asks for
    // no other tool.
    await expect(spawnResultRow).toHaveAttribute('data-span-columns', '0')

    // Its tool_use card -- the row titled with the subagent type -- draws none
    // either. Before the change this row was the one that opened the rail.
    //
    // Restricted to an AGENT row: the prompt above contains the literal
    // "general-purpose", so the user's own message row matches AGENT_TYPE too,
    // and it sits FIRST. Without this filter the assertion reads that row,
    // which never draws a rail, and passes however the spawn card renders.
    const spawnCardRow = page
      .locator('[data-span-columns]:visible')
      .filter({ has: page.locator(ASSISTANT_BUBBLE_SELECTOR) })
      .filter({ hasText: AGENT_TYPE })
      .first()
    await expect(spawnCardRow).toBeVisible()
    await expect(spawnCardRow).toHaveAttribute('data-span-columns', '0')
  })
})
