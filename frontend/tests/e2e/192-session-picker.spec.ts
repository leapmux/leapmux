import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import {
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  loginViaToken,
  openMenu,
  openWorkspace,
  sendMessage,
} from './helpers/ui'
import {
  closeAgentViaAPI,
  createGitRepo,
  listAgentsViaAPI,
  openNewAgentDialog,
  setWorkingDir,
  waitForWorker,
} from './helpers/worktree'

const SESSION_MENU = 'session-select-menu'
const NEW_SESSION_ROW = 'Start a new session'

/**
 * The resume field's session picker, end to end.
 *
 * Every working directory here is a repository the test CREATES under the run's
 * own data directory, and that is what makes the assertions deterministic. The
 * picker merges the worker's records with the selected provider's on-disk
 * store, so a shared directory would let a developer's real `~/.claude` history
 * decide the counts. A directory that never existed before holds exactly what
 * this test put in it.
 *
 * A second agent is kept OPEN in another directory throughout. It keeps a tab
 * in the workspace so the New Agent dialog stays reachable after the subject
 * tab is closed, and it doubles as the negative case: its session belongs to a
 * different directory and must never appear in the subject's list.
 */
test.describe('Session picker in the New Agent dialog', () => {
  test('offers a closed session, hides the open one, and resumes what was picked', async ({
    page,
    leapmuxServer,
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
    await page.locator('[data-testid="tab"][data-tab-type="agent"]')
      .filter({ hasText: 'Subject' })
      .first()
      .click()
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // A turn, so the worker records a resume handle: an agent that never spoke
    // has no session to offer.
    await sendMessage(page, ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page)

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
    const subject = agents.find(a => a.title === 'Subject')
    expect(subject).toBeDefined()

    // While the subject tab is OPEN its session must not be offered: a live
    // process is attached to that handle, and a second one against the same
    // session store corrupts it. With nothing left to offer, the field falls
    // back to its text input rather than showing a menu that cannot resume.
    await openNewAgentDialog(page)
    await waitForWorker(page)
    const firstDialog = page.getByRole('dialog')
    await setWorkingDir(page, subjectDir)
    await expect(firstDialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await expect(firstDialog.getByTestId(`${SESSION_MENU}-trigger`)).toHaveCount(0)
    await firstDialog.getByRole('button', { name: 'Cancel' }).click()

    // Closing the tab releases the handle, and the picker offers it.
    await closeAgentViaAPI(hubUrl, adminToken, workerId, subject!.id)

    await openNewAgentDialog(page)
    await waitForWorker(page)
    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, subjectDir)

    const trigger = dialog.getByTestId(`${SESSION_MENU}-trigger`)
    await expect(trigger).toBeEnabled()
    await openMenu(dialog, SESSION_MENU)

    // Exactly one resumable session plus the row that withdraws a pick. The
    // Keeper's session is still open AND in another directory, so it is absent
    // on both counts.
    const options = dialog.getByTestId(SESSION_MENU).getByRole('menuitemradio')
    await expect(options).toHaveCount(2)
    await expect(options.first()).toHaveText(NEW_SESSION_ROW)

    const sessionRow = options.nth(1)
    const sessionLabel = (await sessionRow.textContent())?.trim() ?? ''
    await sessionRow.click()
    await expect(trigger).toContainText(sessionLabel)

    await dialog.getByRole('button', { name: 'Create' }).click()
    await expect(page.getByRole('heading', { name: 'New Agent' })).toBeHidden()

    // The resumed tab reaches the worker and takes a turn, which proves the
    // handle the picker sent is one the provider accepts.
    await sendMessage(page, ARITHMETIC_PROMPT)
    await expectAssistantAnswer(page)
  })

  test('falls back to the session-id input in a directory with no history', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const emptyDir = createGitRepo(dataDir, 'session-picker-empty')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Empty Picker WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, emptyDir)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await openNewAgentDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, emptyDir)

    // Nothing to pick, so the field keeps the text input. Deleting that
    // fallback would make resume impossible here.
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await expect(dialog.getByLabel('Resume an existing session')).toBeVisible()
  })
})
