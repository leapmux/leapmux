/**
 * 170 — Claude Code subagent + background tasks (the full flow).
 *
 * Covers:
 *
 * - The registry section that a spawn opens.
 * - The subagent tab, which opens beside the parent.
 * - The isolation of the parent and child transcripts, read from the worker.
 * - The Interrupt control of a subagent tab, which stops that subagent alone
 *   through the CLI's `stop_task` control request.
 * - A background shell row.
 *
 * Claude forwards a subagent's own text to the worker
 * (`--forward-subagent-text`), so the child transcript holds it.
 *
 * The row's child-agent-id lands through EnsureChildAgent, which the section
 * renders asynchronously, so the spec polls for a non-empty id before it clicks.
 * Every spawn and every command is scripted, so a missing row fails the spec.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { backgroundBashToolCall, spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  backgroundTasksSection,
  exerciseChildInterrupt,
  exerciseTextGoalQueue,
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  HELD_CHILD_TASK,
  listAgents,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { expectClipsLongText, expectClipsToOneLine, sendMessage, waitForAgentIdle } from './helpers/ui'

test.describe('Claude subagent background tasks', () => {
  test('routes session-goal commands through the input queue', async ({
    authenticatedWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedWorkspace
    // The goal commands drive turns this test does not count.
    await modelScript.fallback({ text: 'Understood.' })
    // Claude starts lazily. Its startup frame advertises /goal after this turn.
    await modelScript.queue({ text: 'ready' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: ready'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await exerciseTextGoalQueue(page, {
      objective: 'Wait for the Claude goal route unlock.',
      clearCommand: '/goal clear',
    })
  })

  test('subagent spawn creates a registry row, a child tab, and isolates the transcript', async ({
    authenticatedWorkspace,
    page,
    leapmuxServer,
    modelScript,
  }) => {
    void authenticatedWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    // 1. Precondition: no registry section yet.
    await expectNoRegistryRows(page)

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
    await waitForAgentIdle(page, 180_000)

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

  // The Claude Code provider stops one subagent alone through the CLI's own
  // `stop_task` control request (`agent.ChildInterrupter`). So the worker
  // states `accepts_interrupt: true`, and the child's tab offers Interrupt
  // while the child works. A stop that WE asked for is a user interrupt, so
  // the closing divider reads "Subagent interrupted" and the row closes as
  // `interrupted`.
  test('the Interrupt control of a working subagent\'s tab stops that subagent alone', async ({
    authenticatedWorkspace,
    page,
    modelScript,
  }) => {
    void authenticatedWorkspace
    await expectNoRegistryRows(page)
    await exerciseChildInterrupt(page, modelScript, {
      provider: AgentProvider.CLAUDE_CODE,
      childTurn: { user: HELD_CHILD_TASK },
    })
  })

  test('background shell appears as a non-clickable shell row', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    // The command is long on purpose: a shell row's TITLE is the command, and
    // the clipping assertions below need one that reaches the edge.
    await modelScript.queue({
      toolCalls: [backgroundBashToolCall(
        AgentProvider.CLAUDE_CODE,
        'bg-shell',
        'sleep 3 && echo BG-MARKER-A-DELIBERATELY-LONG-COMMAND-THAT-REACHES-THE-EDGE-OF-THE-SECTION',
      )],
    })
    await modelScript.queue({ text: 'The command runs in the background.' })
    await sendMessage(page, modelScript.prompt('Start the background shell probe.'))
    await modelScript.waitForSteps(2)

    // Wait for the ROW, not for the agent to go idle. A running background task
    // keeps the thinking indicator up on purpose -- an active registry row IS
    // the agent still working -- so waiting for idle here races the very thing
    // the test is about, and times out whenever the shell outlives the turn.
    // The row is the observable this test wants anyway.
    const shellRow = await requireRegistryRow(page, 'shell')

    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(shellRow!).toHaveAttribute('data-child-agent-id', '')
    // A static row is a <div>. It must render at the same weight as the
    // clickable <button> row that this spec's first test pins.
    await expect(shellRow!).toHaveCSS('font-weight', '400')

    // A shell row's title is a COMMAND, which is long and rarely breakable, so
    // this is where the section used to grow a horizontal scrollbar. The title
    // is clipped now, and nothing above it scrolls sideways. The composed
    // vanilla-extract rules only resolve in a real browser, so a unit test can
    // see the classes but never this outcome.
    //
    // The declarations alone discriminate here -- the title WRAPPED before, so
    // it declared no `nowrap` -- and `expectClipsLongText` then measures the
    // outcome under a title long enough to reach the edge whatever the model
    // sent.
    const title = shellRow!.locator('[class*="taskTitle"]').first()
    await expectClipsToOneLine(title)
    await expectClipsLongText(title)
  })
})
