import { expect } from '@playwright/test'
import { WS_CHANNEL_ROUTE } from '../../../src/generated/contracts/wire'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { registerClaudeChildReportRules } from '../helpers/claudeChildReportRule'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { readNativeSidebarSnapshot } from '../helpers/nativeSidebarSnapshot'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { backgroundTasksSection, expectNoRegistryRows, HELD_CHILD_TASK, openHeldChildTab, requireRegistryRow } from '../helpers/subagentRegistry'
import { expectClipsLongText, expectClipsToOneLine, sendMessage, tabById } from '../helpers/ui'
import { closeAgentViaAPI } from '../helpers/worktree'

/** Test native task rows, title clipping, and registry hydration. */
claudeTest.describe('Claude subagent background tasks', () => {
  claudeTest('background shell appears as a non-clickable shell row', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace
    // A long command lets the test measure title clipping at the section edge.
    const start = await modelScript.queue({
      toolCalls: [backgroundBashToolCall(
        AgentProvider.CLAUDE_CODE,
        'bg-shell',
        'sleep 3 && echo BG-MARKER-A-DELIBERATELY-LONG-COMMAND-THAT-REACHES-THE-EDGE-OF-THE-SECTION',
      )],
    }, { text: 'The command runs in the background.' })
    await sendMessage(page, modelScript.prompt('Start the background shell probe.'))
    await modelScript.waitForSteps(start + 2)

    // A running background task keeps the agent busy. Wait for its row instead of idle.
    const shellRow = await requireRegistryRow(page, 'shell')

    await expect(backgroundTasksSection(page)).toBeVisible()
    await expect(shellRow).toHaveAttribute('data-child-agent-id', '')
    // Static shell rows use the same font weight as clickable subagent rows.
    await expect(shellRow).toHaveCSS('font-weight', '400')

    // The native command must stay on one line and clip inside the section.
    // Browser measurements verify the composed styles and the actual overflow.
    const title = shellRow.locator('[class*="taskTitle"]').first()
    await expectClipsToOneLine(title)
    await expectClipsLongText(title)
  })
})

/** The Worker registry must govern an empty-state assertion before browser hydration. */
claudeTest('refuses an early empty DOM while an actual native task remains in the Worker registry', async ({ page, modelScript, leapmuxServer, native: context }) => {
  const child = await openHeldChildTab(context, { childTurn: { user: HELD_CHILD_TASK }, rootTurnsAfterSpawn: [{ text: 'The real child completed after registry hydration.' }] })
  const forwards: Array<() => void> = []
  let held = true
  let sockets = 0
  await withCleanup(async () => {
    await registerClaudeChildReportRules(modelScript, {
      spawnCallId: 'spawn-held-child',
      report: 'One, two, three.',
      reply: 'The real child report arrived after registry hydration.',
      completionStatus: 'completed',
      completionReply: 'The native child completion notification arrived.',
    })
    const before = await readNativeSidebarSnapshot(context, child.parentId)
    expect(before.backgroundTasks).toHaveLength(1)
    expect(before.backgroundTasks[0]).toMatchObject({ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: child.childId })
    await tabById(page, child.parentId).click()
    await page.routeWebSocket(url => url.pathname === WS_CHANNEL_ROUTE, (browser) => {
      sockets += 1
      const server = browser.connectToServer()
      server.onMessage((message) => {
        const forward = () => browser.send(message)
        if (held)
          forwards.push(forward)
        else
          forward()
      })
    })
    await page.reload()
    await expect.poll(() => sockets).toBeGreaterThan(0)
    await expect.poll(() => forwards.length).toBeGreaterThan(0)
    await expect(page.locator('[data-testid="bg-task-row"]')).toHaveCount(0)
    const actual = await readNativeSidebarSnapshot(context, child.parentId)
    expect(actual.backgroundTasks).toHaveLength(1)
    expect(actual.backgroundTasks[0]?.status).toBe(BackgroundTaskStatus.RUNNING)
    await expect(expectNoRegistryRows(page, leapmuxServer)).rejects.toThrow(/registry|task|rows/i)
  }, async () => {
    try {
      await finishCleanup([
        child.release(),
        closeAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, child.parentId),
      ])
    }
    finally {
      held = false
      for (const forward of forwards.splice(0))
        forward()
    }
  })
})
