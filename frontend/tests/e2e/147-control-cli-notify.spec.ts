/**
 * End-to-end coverage of `leapmux control notify`.
 *
 * Go tests cover the request, the limits and the tab that the Worker stamps
 * from the control token, with fake Workers. This spec runs the CLI against the
 * real Hub and Worker, and observes the toast in the browser.
 *
 * A window hears a Worker only while one of its tabs runs there. The fixture's
 * agent tab runs on the suite's Worker, so the window hears it.
 */

import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { CLIError, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { typeInTerminal, waitForTerminalReady } from './helpers/terminal'
import { expectToastRecorded } from './helpers/toast'
import { tabById, workspaceRow } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

/** The result of `terminal open`. */
interface OpenedTerminal {
  id: string
}

interface NotificationReceipt {
  id: string
  worker_id: string
}

/** The visible toast whose title is `title`. */
function notificationToast(page: Page, title: string) {
  return page.locator('output[data-variant]').filter({
    has: page.locator('.toast-title', { hasText: title }),
  })
}

test.describe('control CLI notify', () => {
  test('shows a notification from outside a tab as a toast with its text', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await expect(tabById(page, authenticatedWorkspace.agentId)).toBeVisible()

    await runCLI(cli, [
      'notify',
      'Build finished',
      '--body',
      'All 42 tests passed',
      '--worker',
      leapmuxServer.workerId,
    ])

    await expectToastRecorded(page, 'Build finished')
    const toast = notificationToast(page, 'Build finished')
    await expect(toast.locator('.toast-body')).toHaveText('All 42 tests passed')
    // The CLI ran outside every tab, so the notification names none.
    await expect(toast.locator('.toast-source')).toHaveCount(0)
  })

  test('names the tab whose process sent the notification', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--title',
      'Notifier',
    ]) as OpenedTerminal
    // The toast names a tab that this window knows by its title, so the title
    // must be here before the notification is.
    const terminalTab = tabById(page, opened.id)
    await expect(terminalTab).toContainText('Notifier')
    await terminalTab.click()
    await waitForTerminalReady(page)

    // The CLI in the tab reaches its own Worker through the tab's socket, and
    // the Worker stamps the tab from the socket's token.
    await typeInTerminal(page, '"$LEAPMUX_CONTROL_CLI" notify "Tests passed in the terminal"')

    await expectToastRecorded(page, 'Tests passed in the terminal')
    await expect(notificationToast(page, 'Tests passed in the terminal').locator('.toast-source')).toHaveText('From Notifier')
  })

  test('opens an addressed tab and clears a notification by ID', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    const opened = await runCLI(cli, [
      'terminal',
      'open',
      '--workspace',
      authenticatedWorkspace.workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--title',
      'Deployment console',
    ]) as OpenedTerminal
    await expect(tabById(page, opened.id)).toBeVisible()
    await tabById(page, authenticatedWorkspace.agentId).click()

    const addressed = await runCLI(cli, [
      'notify',
      'Deployment ready',
      '--tab',
      opened.id,
    ]) as NotificationReceipt
    expect(addressed.id).toMatch(/^[a-z0-9]{48}$/i)
    expect(addressed.worker_id).toBe(leapmuxServer.workerId)
    const toast = notificationToast(page, 'Deployment ready')
    await expect(toast.locator('.toast-source')).toHaveCount(0)
    await toast.getByRole('button', { name: 'Open Deployment console' }).click()
    await expect(tabById(page, opened.id)).toHaveAttribute('aria-selected', 'true')

    const other = await runCLI(cli, [
      'notify',
      'Deployment cleared',
      '--tab',
      opened.id,
    ]) as NotificationReceipt
    expect(other.id).not.toBe(addressed.id)
    const otherToast = notificationToast(page, 'Deployment cleared')
    await expect(otherToast).toBeVisible()
    await runCLI(cli, [
      'notification',
      'clear',
      other.id,
      '--worker',
      other.worker_id,
    ])
    await expect(otherToast).toHaveCount(0)
    const recent = await runCLI(cli, [
      'notification',
      'list',
      '--worker',
      addressed.worker_id,
    ]) as { notifications: { id: string, tab_id?: string }[] }
    expect(recent.notifications).toContainEqual(expect.objectContaining({ id: addressed.id, tab_id: opened.id }))
    expect(recent.notifications).not.toContainEqual(expect.objectContaining({ id: other.id }))

    // Negative guards: blank title and unknown notification ID must fail with structured CLIError codes.
    await expect(runCLI(cli, [
      'notify',
      '   ',
      '--worker',
      leapmuxServer.workerId,
    ])).rejects.toMatchObject({ code: 'usage' })
    await expect(runCLI(cli, [
      'notification',
      'clear',
      'nonexistent-notification-id-e2e',
      '--worker',
      leapmuxServer.workerId,
    ])).rejects.toBeInstanceOf(CLIError)
  })

  test('opens a workspace destination that has no tabs', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const cli = await mintCLITokenForAdmin(leapmuxServer)
    await withTestWorkspace(leapmuxServer, 'notification destination', async ({ workspaceId }) => {
      await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toHaveAttribute('data-active', 'true')
      await expect(workspaceRow(page, workspaceId)).toBeVisible()
      expect(await runCLI(cli, [
        'notify',
        'Review workspace',
        '--workspace',
        workspaceId,
        '--worker',
        leapmuxServer.workerId,
      ])).toMatchObject({ workspace_id: workspaceId, worker_id: leapmuxServer.workerId })
      await notificationToast(page, 'Review workspace').getByRole('button', { name: 'Open workspace' }).click()
      await expect(workspaceRow(page, workspaceId)).toHaveAttribute('data-active', 'true')
    })
  })
})
