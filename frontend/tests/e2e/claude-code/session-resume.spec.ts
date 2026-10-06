import { expect } from '@playwright/test'
import { typeAHandleLabel } from '../../../src/components/shell/resumeSession'
import { AgentProvider, AgentStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { createFromSessionRow, openNewAgentFor, openSessionMenu, openSoleSessionRow, openStoredSessionRow, sessionMenu, sessionMenuTrigger } from '../helpers/nativeResume'
import { openResumeSubject } from '../helpers/nativeResumePicker'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { agentTabs, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, composerEditor, expectAnyVisible, expectAssistantAnswer, expectSettingsChip, expectUserMessage, interruptButton, loginViaToken, menuOptionLabel, messageBubbles, openWorkspace, reopenWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, settingsBar, sidebarLeaves, visibleOnly, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { closeNativeAgentAndWait, listAgentsViaAPI } from '../helpers/workerTabs'
import { createGitRepo } from '../helpers/worktree'
import { restartHub, restartWorker, stopHub, stopWorker, waitForWorkerOffline } from '../process-control-fixtures'
import { expectAnswerAndTurnEnd, waitForWorkerConnection, withRestartWorkspace } from './workerRestart'

test.describe('worker restart thinking indicator', () => {
  test('should hide thinking indicator when worker goes offline during agent turn', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Thinking Indicator Test' }, async () => {
      // The test stops the worker, so the worker restarts after the test, also when it fails.
      await withCleanup(async () => {
        // Start a turn and hold it open. The test stops the worker while the
        // agent waits, so the answer must not arrive first: against the mock
        // endpoint an unheld turn finishes in milliseconds, and the indicator
        // would be gone before the assertion below ran.
        const step = await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
        await sendMessage(page, modelScript.prompt('Write a very long essay about the history of computing. Make it extremely detailed.'))
        await modelScript.waitForSteps(step + 1)

        // Wait for the thinking indicator while the agent works.
        const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]:visible')
        await expect(thinkingIndicator).toBeVisible()

        // Stop the worker while agent is working
        await stopWorker(separateHubWorker)
        await waitForWorkerOffline(separateHubWorker)

        // Thinking indicator should disappear (agent status becomes INACTIVE)
        await expect(thinkingIndicator).not.toBeVisible()

        // Interrupt button should also disappear
        await expect(interruptButton(page)).toHaveCount(0)
      }, () => restartWorker(separateHubWorker))
    })
  })

  test('should resume agent after worker restart and new message', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Agent Resume Test' }, async () => {
      // Send a message and wait for a response
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))

      // Wait for the assistant's response
      await expectAnswerAndTurnEnd(page)

      // Stop the worker
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)

      // Thinking indicator should not be visible
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]:visible')
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
        await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
        await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
      })
      expect(sawThinking).toBe(true)
    })
  })
})

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve chat history after hub and worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Full Restart Test' }, async ({ workspaceId }) => {
      const editor = composerEditor(page)
      await expect(editor).toBeVisible()

      // This specification tests persistence across a restart. The dedicated
      // startup-queue specification sends while this overlay is visible.
      await expect(page.locator('[data-testid="agent-startup-overlay"]')).not.toBeVisible()

      // Step 1: Send a message and wait for a response
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))

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
      await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))

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
    })
  })

  test('should preserve agent tab after clicking it post-restart', async ({ separateHubWorker, page }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Restart Tab Click Test' }, async ({ workspaceId }) => {
      // Verify the agent tab is visible
      const agentTab = agentTabs(page)
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
      await expect(composerEditor(page)).toBeVisible()
      await expect(page.locator('[data-testid="agent-startup-overlay"]')).not.toBeVisible()
      await expect(agentTab).toHaveCount(1)

      // Also verify the tab tree leaf is present in the sidebar. A leaf is not a
      // descendant of its workspace row: the tree follows the row as a sibling,
      // which sidebarLeaves reads.
      const treeLeaf = sidebarLeaves(page, workspaceId).filter({ visible: true })
      await expect(treeLeaf).toHaveCount(1)
    })
  })

  test('should not show thinking indicator after full restart during active turn', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Restart Thinking Test' }, async ({ workspaceId }) => {
      // Start a turn and hold it open, so the hub and worker stop while the
      // agent is genuinely mid-turn. An unheld turn against the mock endpoint
      // finishes in milliseconds and the restart would find nothing active.
      const step = await modelScript.queue({ text: 'An essay.', delayMs: 60_000 })
      await sendMessage(page, modelScript.prompt('Write a very long essay about the history of computing. Make it extremely detailed.'))
      await modelScript.waitForSteps(step + 1)

      // Wait for the thinking indicator or streaming to appear (agent is processing)
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]:visible')
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
      await expect(composerEditor(page)).toBeVisible()

      // Thinking indicator should NOT be visible — stale ACTIVE agents
      // are closed on hub startup so the frontend sees INACTIVE status.
      await expect(thinkingIndicator).not.toBeVisible()
    })
  })
})

test.describe('Settings and /clear after Worker restart', () => {
  test('should handle settings changes and /clear after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Worker Restart Settings Test', pinnedMode: true }, async () => {
      // Step 1: Send a message and wait for a response (agent starts)
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))

      // Wait for the assistant's response containing "6912"
      await expectAnswerAndTurnEnd(page)

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

      // Wait for a notification bubble to contain the expected text.
      const waitForNotification = (text: string) =>
        expect(visibleOnly(page.getByText(text))).toBeVisible()

      // Step 3: Change permission mode (Default → Plan Mode)
      await chooseSettingsOption(page, 'permissionMode-plan')

      await expectSettingsChip(page, 'Plan')
      await waitForNotification('Mode (Default → Plan Mode)')
      await waitForSettingsIdle(page)

      // Step 4: Change effort (Medium → High; the e2e catalog sets Medium).
      // Must happen before switching to Haiku, which hides the effort section.
      await chooseSettingsOption(page, 'effort-high')

      await waitForNotification('Effort (Medium → High)')
      await waitForSettingsIdle(page)

      // Step 5: Change model (Sonnet → Haiku)
      await chooseSettingsOption(page, 'model-haiku')

      await waitForNotification('Model (Sonnet → Haiku)')

      // Step 6: Send /clear
      await sendMessage(page, '/clear')

      await waitForNotification('Context cleared')

      // Verify no "Failed to deliver" messages appeared
      const failedMessages = messageBubbles(page).filter({ hasText: 'Failed to deliver' })
      await expect(failedMessages).toHaveCount(0)
    })
  })
})

test.describe('Agent Session Resume', () => {
  test('should resume the agent process on worker restart without a message', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Eager Resume' }, async ({ workspaceId }) => {
      const { hubUrl, adminToken, workerId } = separateHubWorker

      // One exchange is what gets the CLI to report a session id, which is the
      // filter the boot-time sweep applies: a tab whose agent never ran has
      // nothing to restore and is deliberately left cold.
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
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
      await retryUntilPass(async () => {
        const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
        expect(agents.map(a => a.status), 'the restarted Worker starts the one agent of the workspace').toEqual([AgentStatus.ACTIVE])
      })
    })
  })

  test('should deliver control request after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Control Request Restart' }, async () => {
      // Send a message and wait for response (establishes session)
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(composerEditor(page)).toBeVisible()

      // Switch permission mode to Plan Mode via the settings menu
      await chooseSettingsOption(page, 'permissionMode-plan')

      // Verify the mode chip shows Plan Mode — confirms the control request was
      // delivered after the agent was transparently restarted
      await expectSettingsChip(page, 'Plan Mode')
    })
  })

  test('should handle interrupt after worker restart', async ({ separateHubWorker, page, modelScript }) => {
    await withRestartWorkspace(page, separateHubWorker, { prefix: 'Interrupt Restart' }, async () => {
      // Send a message and wait for response (establishes session)
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
      await expectAnswerAndTurnEnd(page)

      // Stop the worker and wait for the browser connection to change twice.
      await stopWorker(separateHubWorker)
      await waitForWorkerConnection(page, false)
      await restartWorker(separateHubWorker)
      await waitForWorkerConnection(page, true)

      // Wait for editor to be visible (worker reconnected)
      await expect(composerEditor(page)).toBeVisible()

      // Send another message to confirm agent is alive after restart
      await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))

      // Wait for response — verifies normal operation post-restart
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    })
  })
})

test.describe('Agent Settings', () => {
  test.describe('worker restart', () => {
    test('settings restored after worker restart', async ({ authenticatedWorkspace, separateHubWorker, page, modelScript }) => {
      const editor = composerEditor(page)
      await expect(editor).toBeVisible()

      const trigger = settingsBar(page)
      await expect(trigger).toBeVisible()

      // Send a message to establish a session ID.
      const warmup = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
      await modelScript.waitForSteps(warmup + 1)

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
      const relaunch = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
      await modelScript.waitForSteps(relaunch + 1)

      // 6912 only appears in this response (the warmup answered 3333), so scanning
      // all bubbles for it is robust to the trailing "Took Ns" meta bubble that
      // .last() would otherwise race.
      await expectAssistantAnswer(page)

      // Verify Plan Mode is still selected after worker restart
      await expectSettingsChip(page, 'Plan Mode')
    })
  })
})

claudeTest.describe('Session picker in the New Agent dialog', () => {
  claudeTest('offers a closed session, hides the open one, and resumes what was picked', async ({
    page,
    leapmuxServer,
    modelScript,
  }) => {
    // The helper selects the subject explicitly. Which tab the app activates on load is not this feature's contract,
    // and guessing it would make the turn below land in the wrong directory.
    const { subjectId, subjectDir } = await openResumeSubject({ page, modelScript, leapmuxServer }, { label: 'Session Picker' })
    await expect(composerEditor(page)).toBeVisible()

    // A turn, so the worker records a resume handle: an agent that never spoke
    // has no session to offer.
    const original = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(original + 1)
    await expectAssistantAnswer(page)

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
    await closeNativeAgentAndWait({ leapmuxServer }, subjectId)

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
    // for as long as the dialog stayed open. The row label comes from the app's
    // own declaration, so the wording and this locator cannot drift. `false`:
    // the Claude provider's session is an id, not a file path, so only that
    // form of the label is correct here.
    await (await openSessionMenu(dialog))
      .getByRole('menuitemradio', { name: typeAHandleLabel(false) })
      .click()
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await dialog.getByTestId('session-field-pick-from-list').click()
    await expect(trigger).toBeVisible()
    // The way back withdraws the pick, so the field starts from the top.
    await expect(trigger).toHaveAttribute('data-value', '')

    await createFromSessionRow(dialog, await openStoredSessionRow(dialog, sessionValue), sessionValue)

    // The resumed tab reaches the worker and takes a turn, which proves the
    // handle the picker sent is one the provider accepts.
    const resumed = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(resumed + 1)
    await expectAssistantAnswer(page)
  })

  claudeTest('falls back to the session-id input in a directory with no history', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const emptyDir = createGitRepo(dataDir, 'session-picker-empty')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Empty Picker WS')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, emptyDir)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    const dialog = await openNewAgentFor(page, AgentProvider.CLAUDE_CODE, emptyDir)

    // Nothing to pick, so the field keeps the text input. Deleting that
    // fallback would make resume impossible here.
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await expect(dialog.getByLabel('Resume an existing session')).toBeVisible()
  })
})
