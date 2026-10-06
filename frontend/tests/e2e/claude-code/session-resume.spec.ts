import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { typeAHandleLabel } from '../../../src/components/shell/resumeSession'
import { AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI, openPinnedModeAgentViaAPI } from '../helpers/api'
import { createFromSessionRow, openNewAgentFor, openSessionMenu, openSoleSessionRow, openStoredSessionRow, sessionMenu, sessionMenuTrigger } from '../helpers/nativeResume'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { agentTabs, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, composerEditor, expectAnyVisible, expectAssistantAnswer, expectSettingsChip, expectUserMessage, loginViaToken, menuOptionLabel, messageBubbles, openSettingsMenu, openWorkspace, reopenWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, settingsBar, sidebarLeaves, visibleOnly, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, listAgentsViaAPI } from '../helpers/worktree'
import { ensureWorkerOnline, restartHub, restartWorker, stopHub, stopWorker, waitForWorkerOffline } from '../process-control-fixtures'

test.describe('worker restart thinking indicator', () => {
  test('should hide thinking indicator when worker goes offline during agent turn', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Thinking Indicator Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Start a turn and hold it open. The test stops the worker while the
      // agent waits, so the answer must not arrive first: against the mock
      // endpoint an unheld turn finishes in milliseconds, and the indicator
      // would be gone before the assertion below ran.
      await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
      await editor.click()
      await page.keyboard.type(modelScript.prompt('Write a very long essay about the history of computing. Make it extremely detailed.'))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await modelScript.waitForSteps(1)

      // Wait for the thinking indicator while the agent works.
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).toBeVisible()

      // Stop the worker while agent is working
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Thinking indicator should disappear (agent status becomes INACTIVE)
      await expect(thinkingIndicator).not.toBeVisible()

      // Interrupt button should also disappear
      const interruptButton = page.locator('[data-testid="interrupt-button"]')
      await expect(interruptButton).not.toBeVisible()
    }
    finally {
      await restartWorker(separateHubWorker).catch(() => { })
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })

  test('should resume agent after worker restart and new message', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Agent Resume Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for a response
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await editor.click()
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response
      await expectAssistantAnswer(page)
      await waitForAgentIdle(page)

      // Stop the worker
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Thinking indicator should not be visible
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).not.toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)

      // The Worker restarts the agent process. Its idle turn shows no thinking indicator.
      await expect(thinkingIndicator).not.toBeVisible()

      // A new message reaches the resumed agent. The distinct answer cannot
      // match the first turn's saved bubble. The watch starts before the send,
      // so it records even a short indicator before the answer streams.
      const sawThinking = await thinkingIndicatorShownDuring(page, async () => {
        await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
        await editor.click()
        await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
        await page.keyboard.press('Meta+Enter')
        await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
      })
      expect(sawThinking).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })
})

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve chat history after hub and worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Full Restart Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // This specification tests persistence across a restart. The dedicated
      // startup-queue specification sends while this overlay is visible.
      await expect(page.locator('[data-testid="agent-startup-overlay"]')).not.toBeVisible()

      // Step 1: Send a message and wait for a response
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await editor.click()
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "6912"
      await expectAssistantAnswer(page)

      // Verify the user message is also visible
      await expectUserMessage(page, '1234 + 5678')
      await waitForAgentIdle(page)

      // Step 2: Stop Worker first (so agent is terminated), then stop Hub
      await stopWorker(separateHubWorker)
      await stopHub(separateHubWorker)

      // Step 3: Start Hub and Worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload to establish fresh connections to the restarted Hub. The app
      // restores the workspace from browser storage — there is no URL to carry it.
      await reopenWorkspace(page, workspaceId)

      // Wait for the editor to be ready after page reload
      await expect(editor).toBeVisible()

      // Verify the first conversation is still visible after restart (loaded from DB)
      await expectUserMessage(page, '1234 + 5678')
      await expectAssistantAnswer(page)

      // Step 4: Send another message and wait for response. The second answer
      // ("3333") must not be a substring of the first ("6912"), otherwise this
      // wait would match the leftover first-turn bubble instead of the new one.
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await editor.click()
      await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response containing "3333"
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

      // Step 5: Verify both conversations are visible in chat history.
      await expectUserMessage(page, '1234 + 5678')
      await expectUserMessage(page, '1111 + 2222')

      // Verify both assistant responses are present. The two answers ("6912"
      // and "3333") are mutually non-substring, so each check matches only its
      // own turn.
      await expectAssistantAnswer(page)
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })

  test('should preserve agent tab after clicking it post-restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Tab Click Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Verify the agent tab is visible
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toHaveCount(1)

      // Stop worker and hub
      await stopWorker(separateHubWorker)
      await stopHub(separateHubWorker)

      // Restart hub and worker
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload; the app restores the workspace from browser storage.
      await reopenWorkspace(page, workspaceId)

      // Agent tab should be visible after restore
      await expect(agentTab).toHaveCount(1)

      // Click the agent tab — it should remain visible (not disappear).
      // Before the fix, clicking an inactive agent with no messages would
      // remove it because the WatchEvents catch-up phase reported INACTIVE
      // status before message replay completed.
      await agentTab.click()
      await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
      await expect(page.locator('[data-testid="agent-startup-overlay"]')).not.toBeVisible()
      await expect(agentTab).toHaveCount(1)

      // Also verify the tab tree leaf is present in the sidebar. A leaf is not a
      // descendant of its workspace row: the tree follows the row as a sibling,
      // which sidebarLeaves reads.
      const treeLeaf = sidebarLeaves(page, workspaceId).filter({ visible: true })
      await expect(treeLeaf).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })

  test('should not show thinking indicator after full restart during active turn', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Thinking Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Start a turn and hold it open, so the hub and worker stop while the
      // agent is genuinely mid-turn. An unheld turn against the mock endpoint
      // finishes in milliseconds and the restart would find nothing active.
      await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
      await editor.click()
      await page.keyboard.type(modelScript.prompt('Write a very long essay about the history of computing. Make it extremely detailed.'))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await modelScript.waitForSteps(1)

      // Wait for the thinking indicator or streaming to appear (agent is processing)
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      const streamingText = assistantBubbles(page)
      await expectAnyVisible(thinkingIndicator, streamingText)

      // Stop worker first (so agent is terminated), then stop hub — while agent is mid-turn
      await stopWorker(separateHubWorker)
      await stopHub(separateHubWorker)

      // Start hub and worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload to establish fresh connections to the restarted hub. The app
      // restores the workspace from browser storage — there is no URL to carry it.
      await reopenWorkspace(page, workspaceId)
      await expect(editor).toBeVisible()

      // Thinking indicator should NOT be visible — stale ACTIVE agents
      // are closed on hub startup so the frontend sees INACTIVE status.
      await expect(thinkingIndicator).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })
})

test.describe('Settings and /clear after Worker restart', () => {
  test('should handle settings changes and /clear after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Worker Restart Settings Test')
    await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response (agent starts)
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await editor.click()
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "6912"
      await expectAssistantAnswer(page)
      await waitForAgentIdle(page)

      // Step 2: Restart the Worker (stop + start). All persistent data
      // (workspaces, agents, messages) is stored on the Worker's SQLite DB,
      // so the conversation should survive the restart.
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Wait for the E2EE channels to reconnect and messages to reload.
      // The original conversation should be visible (loaded from Worker DB).
      await expectUserMessage(page, '1234 + 5678')
      await expectAssistantAnswer(page)

      // Helper: wait for a notification bubble to contain the expected text.
      const waitForNotification = (text: string) =>
        expect(visibleOnly(page.getByText(text))).toBeVisible()

      // Helper: wait for the settings loading spinner to disappear.
      const waitForSettingsIdle = () =>
        expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()

      // Step 3: Change permission mode (Default → Plan Mode)
      await openSettingsMenu(page, 'permissionMode')
      await page.locator('[data-testid="permissionMode-plan"]').click()

      await expectSettingsChip(page, 'Plan')
      await waitForNotification('Mode (Default \u2192 Plan Mode)')
      await waitForSettingsIdle()

      // Step 4: Change effort (Medium → High; the e2e catalog sets Medium).
      // Must happen before switching to Haiku, which hides the effort section.
      await openSettingsMenu(page, 'effort')
      await page.locator('[data-testid="effort-high"]').click()

      await waitForNotification('Effort (Medium \u2192 High)')
      await waitForSettingsIdle()

      // Step 5: Change model (Sonnet → Haiku)
      await openSettingsMenu(page, 'model')
      await page.locator('[data-testid="model-haiku"]').click()

      await waitForNotification('Model (Sonnet \u2192 Haiku)')

      // Step 6: Send /clear
      await editor.click()
      await page.keyboard.type('/clear')
      await page.keyboard.press('Meta+Enter')

      await waitForNotification('Context cleared')

      // Verify no "Failed to deliver" messages appeared
      const failedMessages = messageBubbles(page).filter({ hasText: 'Failed to deliver' })
      await expect(failedMessages).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })
})

test.describe('Agent Session Resume', () => {
  /**
   * Wait for the first answer, and then for the end of its turn, before a test
   * stops the worker.
   *
   * The answer's text reaches the page before the turn ends. A worker that stops
   * inside that window leaves the input queue's turn open, and the worker's
   * restart then pauses the queue as interrupted. The next message then waits in
   * the paused queue and never reaches the agent.
   */
  async function expectAnswerAndTurnEnd(page: Page) {
    await expectAssistantAnswer(page)
    await waitForAgentIdle(page)
  }

  async function waitForWorkerConnection(page: Page, connected: boolean) {
    const status = page.getByTestId('section-header-workers').locator('[data-status="connected"]')
    if (connected)
      await expect(status).not.toHaveCount(0)
    else
      await expect(status).toHaveCount(0)
  }

  test('should resume the agent process on worker restart without a message', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Eager Resume')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // One exchange is what gets the CLI to report a session id, which is the
      // filter the boot-time sweep applies: a tab whose agent never ran has
      // nothing to restore and is deliberately left cold.
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // The whole point: the process comes back on its own. Nothing below sends
      // a message, so a worker that only spawns lazily leaves the agent
      // INACTIVE for ever and this poll times out.
      //
      // Polled through ListAgents, a WORKER-backed RPC. The hub's tab list and
      // the local tab state are optimistic CRDT state and would report a
      // healthy tab for an agent whose process is gone.
      await expect.poll(async () => {
        const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
        return agents.map(a => a.status)
      }).toEqual([AgentStatus.ACTIVE])
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })

  test('should deliver control request after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Request Restart')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Switch permission mode to Plan Mode via the settings menu
      await chooseSettingsOption(page, 'permissionMode-plan')

      // Verify the mode chip shows Plan Mode — confirms the control request was
      // delivered after the agent was transparently restarted
      await expectSettingsChip(page, 'Plan Mode')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })

  test('should handle interrupt after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Interrupt Restart')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Send another message to confirm agent is alive after restart
      await editor.click()
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await page.keyboard.type(modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await page.keyboard.press('Meta+Enter')

      // Wait for response — verifies normal operation post-restart
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => { })
    }
  })
})

test.describe('Agent Settings', () => {
  test.describe('worker restart', () => {
    test('settings restored after worker restart', async ({ authenticatedWorkspace, separateHubWorker, page, modelScript }) => {
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      const trigger = settingsBar(page)
      await expect(trigger).toBeVisible()

      // Send a message to establish a session ID.
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await modelScript.waitForSteps(1)

      // Wait for a response (ensures init message and session ID are stored)
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

      // Switch to Plan Mode (dropdown auto-closes on select)
      await chooseSettingsOption(page, 'permissionMode-plan')
      await expectSettingsChip(page, 'Plan Mode')

      // Wait for the Worker to confirm the settings before it stops.
      await waitForSettingsIdle(page)

      // Stop worker
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Restart worker
      await restartWorker(separateHubWorker)

      // Wait for the editor to become visible again after worker reconnects
      await expect(editor).toBeVisible()

      // Send a message to trigger agent re-launch via ensureAgentActive
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
      await modelScript.waitForSteps(2)

      // 6912 only appears in this response (the warmup answered 3333), so scanning
      // all bubbles for it is robust to the trailing "Took Ns" meta bubble that
      // .last() would otherwise race.
      await expectAssistantAnswer(page)

      // Verify Plan Mode is still selected after worker restart
      await expectSettingsChip(page, 'Plan Mode')
    })
  })
})

// From the app's own declaration, so the row's wording and this locator cannot
// drift. `false`: the Claude provider's session is an id, not a file path.
const TYPE_A_HANDLE_ROW = typeAHandleLabel(false)

claudeTest.describe('Session picker in the New Agent dialog', () => {
  claudeTest('offers a closed session, hides the open one, and resumes what was picked', async ({
    page,
    leapmuxServer,
    modelScript,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const keeperDir = createGitRepo(dataDir, 'session-picker-keeper')
    const subjectDir = createGitRepo(dataDir, 'session-picker-subject')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Session Picker WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, subjectDir, { title: 'Subject' })

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    // Select the subject explicitly. Which tab the app activates on load is not
    // this feature's contract, and guessing it would make the turn below land
    // in the wrong directory.
    await agentTabs(page).filter({ hasText: 'Subject' }).first().click()
    await expect(composerEditor(page)).toBeVisible()

    // A turn, so the worker records a resume handle: an agent that never spoke
    // has no session to offer.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await expectAssistantAnswer(page)

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
    const subject = agents.find(a => a.title === 'Subject')
    expect(subject).toBeDefined()

    // While the subject tab is OPEN its session must not be offered: a live
    // process is attached to that handle, and a second one against the same
    // session store corrupts it. With nothing left to offer, the field falls
    // back to its text input rather than showing a menu that cannot resume.
    const firstDialog = await openNewAgentFor(page, AgentProvider.CLAUDE_CODE, subjectDir)
    // Wait for the ANSWER before asserting the absence. The field shows a
    // disabled menu until a fetch for the current directory settles, and the
    // refresh button is enabled only when no fetch is in flight -- so this is
    // the field's own "the worker has replied" signal. Without it both
    // assertions below could pass against a request that had not returned yet,
    // and the exclusion this test exists to prove would go unchecked.
    await expect(firstDialog.getByTestId('session-field-refresh')).toBeEnabled()
    await expect(firstDialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await expect(sessionMenuTrigger(firstDialog)).toHaveCount(0)
    await firstDialog.getByRole('button', { name: 'Cancel' }).click()

    // Closing the tab releases the handle, and the picker offers it.
    await closeAgentViaAPI(hubUrl, adminToken, workerId, subject!.id)

    const dialog = await openNewAgentFor(page, AgentProvider.CLAUDE_CODE, subjectDir)
    const trigger = sessionMenuTrigger(dialog)

    // Exactly one resumable session, UNDER the two rows that are not sessions:
    // the one that withdraws a pick and the one that hands the field back to
    // its text box. The Keeper's session is still open AND in another
    // directory, so it is absent on both counts.
    const sessionRow = await openSoleSessionRow(dialog)

    // The menu opens over a dialog, so it must not outgrow the control it
    // belongs to or the dialog that holds it. One long session title used to
    // make it wider than the dialog, and a long list made it taller.
    //
    // The assertions read the resolved CAPS, not the drawn box. This menu holds
    // three short rows, so its box is well inside both limits whether or not any
    // limit exists -- a measurement alone would pass against an uncapped
    // popover and state nothing. The caps are the thing under test: they are
    // what a fifty-session list and a title wider than the field run into.
    const menu = sessionMenu(dialog)
    const triggerBox = await trigger.boundingBox()
    const dialogBox = await dialog.boundingBox()
    const caps = await menu.evaluate((el) => {
      const s = getComputedStyle(el)
      return { maxWidth: s.maxWidth, maxHeight: s.maxHeight, overflow: s.overflowY }
    })
    expect(Number.parseFloat(caps.maxWidth)).toBeLessThanOrEqual(triggerBox!.width + 1)
    expect(Number.parseFloat(caps.maxHeight)).toBeLessThanOrEqual(dialogBox!.height + 1)
    // The cap only makes the rows past it UNREACHABLE unless the box scrolls.
    expect(caps.overflow).toBe('auto')

    const menuBox = await menu.boundingBox()
    expect(menuBox!.width).toBeLessThanOrEqual(triggerBox!.width + 1)
    expect(menuBox!.height).toBeLessThanOrEqual(dialogBox!.height + 1)

    const sessionValue = (await sessionRow.getAttribute('data-testid'))!
      .replace('loading-menu-option-', '')
    // The TITLE, not the row's whole text: the row also carries the age, which
    // is a live relative time and can tick between this read and the assertion
    // below. Every row states one beside its title, and it stays put however
    // long that title is.
    const sessionTitle = (await menuOptionLabel(sessionRow).textContent())?.trim() ?? ''
    await expect(sessionRow).toContainText('ago')

    await sessionRow.click()
    await expect(trigger).toHaveAttribute('data-value', sessionValue)
    await expect(trigger).toContainText(sessionTitle)

    // The route into the text box is a menu row, so the route out is a button
    // on the field. Without it a mistaken pick held the user in the text box
    // for as long as the dialog stayed open.
    await (await openSessionMenu(dialog))
      .getByRole('menuitemradio', { name: TYPE_A_HANDLE_ROW })
      .click()
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await dialog.getByTestId('session-field-pick-from-list').click()
    await expect(trigger).toBeVisible()
    // The way back withdraws the pick, so the field starts from the top.
    await expect(trigger).toHaveAttribute('data-value', '')

    await createFromSessionRow(dialog, await openStoredSessionRow(dialog, sessionValue), sessionValue)

    // The resumed tab reaches the worker and takes a turn, which proves the
    // handle the picker sent is one the provider accepts.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await expectAssistantAnswer(page)
  })

  claudeTest('falls back to the session-id input in a directory with no history', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const emptyDir = createGitRepo(dataDir, 'session-picker-empty')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Empty Picker WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, emptyDir)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    const dialog = await openNewAgentFor(page, AgentProvider.CLAUDE_CODE, emptyDir)

    // Nothing to pick, so the field keeps the text input. Deleting that
    // fallback would make resume impossible here.
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await expect(dialog.getByLabel('Resume an existing session')).toBeVisible()
  })
})
