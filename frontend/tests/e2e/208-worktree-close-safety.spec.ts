import type { Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { RegisterTabPayloadRequestSchema, RegisterTabPayloadResponseSchema } from '../../src/generated/proto/leapmux/v1/worker_private_pb'
import { TabType } from '../../src/generated/proto/leapmux/v1/workspace_pb'
import { fileTabPayload } from '../../src/lib/tabPayload'
import { test } from './fixtures'
import { createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './helpers/api'
import { getUserEventsSubscription, seedTabIntoWorkspace } from './helpers/crdt'
import { deleteWorkspaceViaUI, loginViaToken, openWorkspace, tabById } from './helpers/ui'
import { inspectLastTabCloseViaAPI, waitForAgentStartupViaAPI, waitForWorkerAgentsClosed } from './helpers/workerTabs'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'
import { addWorktree, branchExists, commitFile, createGitRepo, waitForPathDeleted } from './helpers/worktree'

function checkout(dataDir: string, name: string) {
  const repo = createGitRepo(dataDir, `${name}-repo`)
  commitFile(repo, 'frontend/src/committed.txt', 'preserve me\n', 'Add committed contents')
  const branch = `${name}-branch`
  const worktree = addWorktree(repo, dataDir, `${name}-checkout`, branch)
  return {
    repo,
    worktree,
    branch,
    nested: join(worktree, 'frontend', 'src'),
  }
}

function expectPreserved(paths: ReturnType<typeof checkout>, content = 'preserve me\n') {
  expect(existsSync(paths.worktree)).toBe(true)
  const listing = execFileSync('git', ['worktree', 'list', '--porcelain'], { cwd: paths.repo, encoding: 'utf8' })
  expect(listing).toContain(`worktree ${paths.worktree.replaceAll('\\', '/')}\n`)
  expect(listing).not.toContain('locked')
  expect(branchExists(paths.repo, paths.branch)).toBe(true)
  expect(readFileSync(join(paths.nested, 'committed.txt'), 'utf8')).toBe(content)
}

function closeDialog(page: Page) {
  return page.getByRole('dialog').filter({
    has: page.getByRole('heading', { name: 'Close Last Tab' }),
  })
}

async function clickClose(page: Page, agentId: string) {
  const tab = tabById(page, agentId)
  await expect(tab).toBeVisible()
  await tab.getByTestId('tab-close').dispatchEvent('click')
}

test.describe('worktree close safety', () => {
  test('disables deletion for an untracked file-tab worktree', async ({ page, leapmuxServer }) => {
    const paths = checkout(leapmuxServer.dataDir, 'safety-untracked-file')
    const { hubUrl, adminToken: cookie, workerId } = leapmuxServer
    const userEvents = await getUserEventsSubscription(hubUrl, cookie)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, cookie, 'Untracked file worktree')
    const channel = await getTestChannel(hubUrl, cookie)
    const tabId = `file-untracked-${workspaceId}`
    await channel.callWorker(workerId, 'RegisterTabPayload', RegisterTabPayloadRequestSchema, RegisterTabPayloadResponseSchema, {
      tabId,
      payload: fileTabPayload(join(paths.nested, 'committed.txt'), paths.worktree),
    })
    await seedTabIntoWorkspace({
      hubUrl,
      cookie,
      workspaceId,
      workerId,
      tabId,
      tabType: TabType.FILE,
      userEvents,
    })
    await loginViaToken(page, cookie)
    await openWorkspace(page, workspaceId)
    await clickClose(page, tabId)
    const dialog = closeDialog(page)
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Delete worktree' })).toBeDisabled()
    await expect(dialog.getByText('LeapMux does not track this worktree.', { exact: false })).toBeVisible()
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    expectPreserved(paths)
  })

  test('allows root deletion when an unrelated tab directory is missing', async ({ page, leapmuxServer }) => {
    const unrelated = createGitRepo(leapmuxServer.dataDir, 'safety-unrelated-missing')
    const other = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Missing unrelated directory', { workingDir: unrelated })
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, other.workspaceId)
    rmSync(unrelated, { recursive: true })
    const paths = checkout(leapmuxServer.dataDir, 'safety-valid-root')
    const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Valid root directory', { workingDir: paths.worktree })
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    await clickClose(page, agentIds[0]!)
    const dialog = closeDialog(page)
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete worktree' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()
    await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
    await waitForPathDeleted(paths.worktree)
    await expect.poll(() => branchExists(paths.repo, paths.branch)).toBe(false)
  })

  for (const dirty of [false, true]) {
    test(`closes a ${dirty ? 'dirty' : 'clean'} nested agent without Git confirmation`, async ({ page, leapmuxServer }) => {
      const paths = checkout(leapmuxServer.dataDir, dirty ? 'safety-dirty' : 'safety-clean')
      const content = dirty ? 'unsaved contents\n' : 'preserve me\n'
      if (dirty)
        writeFileSync(join(paths.nested, 'committed.txt'), content)
      const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Nested checkout', { workingDir: paths.nested })
      await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
      await loginViaToken(page, leapmuxServer.adminToken)
      await openWorkspace(page, workspaceId)
      await clickClose(page, agentIds[0]!)
      await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
      await expect(closeDialog(page)).not.toBeVisible()
      await expect(tabById(page, agentIds[0]!)).not.toBeVisible()
      expectPreserved(paths, content)
    })
  }

  test('preserves a tracked checkout when its root closes before its nested sibling', async ({ page, leapmuxServer }) => {
    const paths = checkout(leapmuxServer.dataDir, 'safety-siblings')
    // Open the nested tab first, before a root tab creates the tracking record.
    const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Nested sibling', { workingDir: paths.nested })
    const rootId = await openAgentViaAPI(leapmuxServer, workspaceId, paths.worktree)
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId, 2)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    await clickClose(page, rootId)
    await waitForWorkerAgentsClosed({ leapmuxServer }, [rootId])
    await expect(closeDialog(page)).not.toBeVisible()
    expectPreserved(paths)
    // Tracking survives the root close. The remaining nested tab still cannot delete it.
    await clickClose(page, agentIds[0]!)
    await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
    await expect(closeDialog(page)).not.toBeVisible()
    expectPreserved(paths)
  })

  test('requires a confirmed root choice to delete an existing linked worktree', async ({ page, leapmuxServer }) => {
    const paths = checkout(leapmuxServer.dataDir, 'safety-confirm')
    const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Root confirmation', { workingDir: paths.worktree })
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    await clickClose(page, agentIds[0]!)
    const dialog = closeDialog(page)
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete worktree' }).click()
    await expect(dialog.getByRole('button', { name: 'Confirm?' })).toBeVisible()
    expectPreserved(paths)
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    await expect(tabById(page, agentIds[0]!)).toBeVisible()
    const inspected = await inspectLastTabCloseViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, TabType.AGENT, agentIds[0]!)
    expect(inspected.shouldPrompt).toBe(true)
    expectPreserved(paths)
    await clickClose(page, agentIds[0]!)
    await dialog.getByRole('button', { name: 'Delete worktree' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()
    await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
    await waitForPathDeleted(paths.worktree)
    await expect.poll(() => branchExists(paths.repo, paths.branch)).toBe(false)
  })

  test('closes a clean main working tree on an arbitrary branch without Git confirmation', async ({ page, leapmuxServer }) => {
    const repo = createGitRepo(leapmuxServer.dataDir, 'safety-main')
    execFileSync('git', ['branch', '-m', 'ordinary-branch'], { cwd: repo })
    const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Main working tree', { workingDir: repo })
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    await clickClose(page, agentIds[0]!)
    await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
    await expect(closeDialog(page)).not.toBeVisible()
    expect(existsSync(repo)).toBe(true)
    expect(branchExists(repo, 'ordinary-branch')).toBe(true)
    expect(readFileSync(join(repo, 'README.md'), 'utf8')).toBe('# Test\n')
  })

  test('preserves Git artifacts after workspace deletion completes Worker teardown', async ({ page, leapmuxServer }) => {
    const paths = checkout(leapmuxServer.dataDir, 'safety-workspace')
    const { workspaceId, agentIds } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Delete nested workspace', { workingDir: paths.nested })
    await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspaceId)
    await deleteWorkspaceViaUI(page, workspaceId)
    await waitForWorkerAgentsClosed({ leapmuxServer }, agentIds)
    expectPreserved(paths)
  })
})
