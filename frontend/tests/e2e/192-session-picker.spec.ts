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
const TYPE_A_HANDLE_ROW = 'Enter a session ID…'

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
    // Wait for the ANSWER before asserting the absence. The field shows a
    // disabled menu until a fetch for the current directory settles, and the
    // refresh button is enabled only when no fetch is in flight -- so this is
    // the field's own "the worker has replied" signal. Without it both
    // assertions below could pass against a request that had not returned yet,
    // and the exclusion this test exists to prove would go unchecked.
    await expect(firstDialog.getByTestId('session-field-refresh')).toBeEnabled()
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

    // Exactly one resumable session, UNDER the two rows that are not sessions:
    // the one that withdraws a pick and the one that hands the field back to
    // its text box. The Keeper's session is still open AND in another
    // directory, so it is absent on both counts.
    const options = dialog.getByTestId(SESSION_MENU).getByRole('menuitemradio')
    await expect(options).toHaveCount(3)
    await expect(options.first()).toHaveText(NEW_SESSION_ROW)
    await expect(options.nth(1)).toHaveText(TYPE_A_HANDLE_ROW)

    // The menu opens over a dialog, so it must not outgrow the control it
    // belongs to or the dialog that holds it. One long session title used to
    // make it wider than the dialog, and a long list made it taller.
    const triggerBox = await trigger.boundingBox()
    const dialogBox = await dialog.boundingBox()
    const menuBox = await dialog.getByTestId(SESSION_MENU).boundingBox()
    expect(menuBox!.width).toBeLessThanOrEqual(triggerBox!.width + 1)
    expect(menuBox!.height).toBeLessThanOrEqual(dialogBox!.height + 1)

    const sessionRow = options.nth(2)
    const sessionValue = (await sessionRow.getAttribute('data-testid'))!
      .replace('loading-menu-option-', '')
    // The TITLE, not the row's whole text: the row also carries the age, which
    // is a live relative time and can tick between this read and the assertion
    // below. Every row states one beside its title, and it stays put however
    // long that title is.
    const sessionTitle = (await sessionRow
      .getByTestId(`loading-menu-option-${sessionValue}-label`)
      .textContent())?.trim() ?? ''
    await expect(sessionRow).toContainText('ago')

    await sessionRow.click()
    await expect(trigger).toHaveAttribute('data-value', sessionValue)
    await expect(trigger).toContainText(sessionTitle)

    // The route into the text box is a menu row, so the route out is a button
    // on the field. Without it a mistaken pick held the user in the text box
    // for as long as the dialog stayed open.
    await openMenu(dialog, SESSION_MENU)
    await dialog.getByTestId(SESSION_MENU)
      .getByRole('menuitemradio', { name: TYPE_A_HANDLE_ROW })
      .click()
    await expect(dialog.getByPlaceholder(/^Session ID/)).toBeVisible()
    await dialog.getByTestId('session-field-pick-from-list').click()
    await expect(trigger).toBeVisible()
    // The way back withdraws the pick, so the field starts from the top.
    await expect(trigger).toHaveAttribute('data-value', '')

    await openMenu(dialog, SESSION_MENU)
    await dialog.getByTestId(`loading-menu-option-${sessionValue}`).click()
    await expect(trigger).toHaveAttribute('data-value', sessionValue)

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
