import path from 'node:path'
import { expect, test } from './fixtures'
import { API_POLL_INTERVAL_MS, createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { branchGroupRow, clickBranchMenuItem, loginViaToken, openWorkspace } from './helpers/ui'
import {
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  listAgentsViaAPI,
  listTerminalsViaAPI,
  openNewAgentDialog,
  setWorkingDir,
  waitForWorker,
} from './helpers/worktree'

const frontendDir = path.resolve(import.meta.dirname, '../..')

/**
 * Polls the hub until a tab with `title` appears among `list`'s results.
 *
 * The local tab list is optimistic CRDT state, so asserting against the
 * sidebar would pass on what this client wrote rather than on what the worker
 * stored -- and the title the worker stores is the CLEANED one, which is the
 * whole point of sending it. This reads the worker-backed RPC instead.
 */
async function waitForTabTitle(
  list: () => Promise<Array<{ title: string }>>,
  title: string,
  timeoutMs = 15_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  let seen: string[] = []
  while (Date.now() < deadline) {
    seen = (await list()).map(t => t.title)
    if (seen.includes(title))
      return
    await new Promise(resolve => setTimeout(resolve, API_POLL_INTERVAL_MS))
  }
  throw new Error(`timed out waiting for a tab titled ${JSON.stringify(title)}; saw ${JSON.stringify(seen)}`)
}

test.describe('Tab title in the create dialogs', () => {
  test('new agent dialog pre-fills a pooled title, re-rolls it, and sends what the user typed', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Agent Title WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await openNewAgentDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    const titleInput = dialog.getByTestId('title-input')

    // The default carries the shape the worker's plan-mode auto-rename
    // accepts, so a title left alone stays overwritable by a plan title.
    await expect(titleInput).toHaveValue(/^Agent [A-Z][A-Za-z]+$/)

    // Re-roll until the value moves: the pool holds hundreds of names, so one
    // click can legitimately draw the same one.
    const first = await titleInput.inputValue()
    for (let i = 0; i < 50 && await titleInput.inputValue() === first; i++)
      await dialog.getByTestId('title-regenerate').click()
    await expect(titleInput).not.toHaveValue(first)
    await expect(titleInput).toHaveValue(/^Agent [A-Z][A-Za-z]+$/)

    // An emptied title blocks submit rather than letting the worker silently
    // re-name the tab from its own pool.
    await titleInput.fill('   ')
    await expect(dialog.getByText('Name must not be empty')).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Create' })).toBeDisabled()

    await titleInput.fill('  Auth   fix  ')
    await setWorkingDir(page, frontendDir)
    await dialog.getByRole('button', { name: 'Create' }).click()

    // The CLEANED title, folded and trimmed -- what the worker stored, read
    // back from the worker rather than from this client's own state.
    await waitForTabTitle(
      () => listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId),
      'Auth fix',
    )
  })

  test('new terminal dialog pre-fills a pooled title and sends what the user typed', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Terminal Title WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New terminal...' }).click()
    await expect(page.getByRole('heading', { name: 'New Terminal' })).toBeVisible()
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    const titleInput = dialog.getByTestId('title-input')
    await expect(titleInput).toHaveValue(/^Terminal [A-Z][A-Za-z]+$/)

    // The Shell field's refresh button: the field used to be a bare label
    // with no room for one, and the hook's manual retry was unreachable.
    await dialog.getByTestId('shell-selector-refresh').click()
    await expect(dialog.getByTestId('shell-select-menu-trigger')).toBeEnabled()

    await titleInput.fill('')
    await expect(dialog.getByText('Name must not be empty')).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Create' })).toBeDisabled()

    await titleInput.fill('Build  logs')
    await setWorkingDir(page, frontendDir)
    await dialog.getByRole('button', { name: 'Create' }).click()

    await waitForTabTitle(
      () => listTerminalsViaAPI(hubUrl, adminToken, workerId, workspaceId),
      'Build logs',
    )
  })

  test('change branch dialog titles the worktree tab and follows the Open as toggle', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'title-change-branch-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Change Branch Title WS',
      repoDir,
      'title-change-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await clickBranchMenuItem(page, branchGroupRow(page), 'Switch to branch...')
    await expect(page.getByRole('heading', { name: 'Change branch' })).toBeVisible()

    const dialog = page.getByRole('dialog')
    // The field belongs to create-worktree only, the one mode that opens a tab.
    await expect(dialog.getByTestId('title-input')).toHaveCount(0)

    await dialog.getByText('Create new worktree', { exact: true }).click()
    const titleInput = dialog.getByTestId('title-input')
    await expect(titleInput).toHaveValue(/^Agent [A-Z][A-Za-z]+$/)

    // The generated prefix follows the toggle, so the tab never carries the
    // other kind's name...
    await dialog.getByRole('radio', { name: 'Terminal' }).click()
    await expect(titleInput).toHaveValue(/^Terminal [A-Z][A-Za-z]+$/)

    // ...but a name the user typed survives the flip.
    await titleInput.fill('Auth fix')
    await dialog.getByRole('radio', { name: 'Agent' }).click()
    await expect(titleInput).toHaveValue('Auth fix')

    await dialog.getByRole('button', { name: 'Apply' }).click()

    await waitForTabTitle(
      () => listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId),
      'Auth fix',
    )
  })
})
