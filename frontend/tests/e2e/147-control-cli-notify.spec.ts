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
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { typeInTerminal, waitForTerminalReady } from './helpers/terminal'
import { expectToastRecorded } from './helpers/toast'
import { tabById } from './helpers/ui'

/** The result of `terminal open`. */
interface OpenedTerminal {
  id: string
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
    await typeInTerminal(page, '"$LEAPMUX_CONTROL_CLI" control notify "Tests passed in the terminal"')

    await expectToastRecorded(page, 'Tests passed in the terminal')
    await expect(notificationToast(page, 'Tests passed in the terminal').locator('.toast-source')).toHaveText('From Notifier')
  })
})
