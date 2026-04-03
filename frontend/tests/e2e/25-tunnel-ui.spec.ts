import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// The tunnel feature requires the desktop app (Wails), which isn't available
// in E2E tests. These tests inject mock tunnel methods on window.go.main.App
// before page load to verify the UI renders correctly when the tunnel API
// is available. The mock is set via addInitScript so that isTunnelAvailable()
// returns true at initial render time (Solid.js doesn't re-track plain globals).

/**
 * Add a page.addInitScript that sets up mock tunnel methods on
 * window.go.main.App. Only the tunnel-specific methods are set —
 * ProxyHTTP is intentionally left out so the app uses its normal transport.
 */
function addTunnelMockInitScript(page: import('@playwright/test').Page) {
  return page.addInitScript(() => {
    const tunnels: Array<{
      id: string
      workerId: string
      type: string
      bindAddr: string
      bindPort: number
      targetAddr: string
      targetPort: number
    }> = []
    let nextId = 1

    // Set up window.go.main.App with tunnel methods only.
    // This makes isTunnelAvailable() return true (checks CreateTunnel)
    // without triggering isWailsApp() (checks ProxyHTTP).
    ;(window as any).go = {
      main: {
        App: {
          CreateTunnel: (config: any) => {
            const tunnel = {
              id: `tunnel-${nextId++}`,
              workerId: config.workerId,
              type: config.type,
              bindAddr: config.bindAddr || '127.0.0.1',
              bindPort: config.bindPort || (config.type === 'socks5' ? 1080 : config.targetPort),
              targetAddr: config.targetAddr || '',
              targetPort: config.targetPort || 0,
            }
            tunnels.push(tunnel)
            return Promise.resolve(tunnel)
          },
          DeleteTunnel: (id: string) => {
            const idx = tunnels.findIndex(t => t.id === id)
            if (idx >= 0)
              tunnels.splice(idx, 1)
            return Promise.resolve()
          },
          ListTunnels: () => {
            return Promise.resolve([...tunnels])
          },
        },
      },
    }
  })
}

/** Open the worker context menu in the Workers sidebar section. */
async function openWorkerMenu(page: import('@playwright/test').Page) {
  const workersSection = page.getByTestId('section-header-workers')
  await expect(workersSection).toBeVisible()
  const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
  if (!isOpen)
    await workersSection.locator('> [role="button"]').click()

  await expect(workersSection.getByText('Local')).toBeVisible()

  const menuButton = workersSection.locator('[aria-expanded]').first()
  await menuButton.click()
  return workersSection
}

/** Open the "Add tunnel" dialog via the worker context menu. */
async function openAddTunnelDialog(page: import('@playwright/test').Page) {
  const workersSection = await openWorkerMenu(page)
  await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()
  const dialog = page.getByTestId('add-tunnel-dialog')
  await expect(dialog).toBeVisible()
  return { workersSection, dialog }
}

test.describe('Tunnel UI', () => {
  // Use manual setup: inject the Wails tunnel mock via addInitScript before
  // navigating so that isTunnelAvailable() returns true at first render.
  test.beforeEach(async ({ page, workspace, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await addTunnelMockInitScript(page)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)
  })

  test('add tunnel menu item appears when tunnel API is available', async ({ page, workspace }) => {
    await openWorkerMenu(page)
    await expect(page.getByRole('menuitem', { name: 'Add tunnel...' })).toBeVisible()
  })

  test('add tunnel dialog opens and has correct fields', async ({ page, workspace }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('target-addr')).toBeVisible()
    await expect(dialog.getByTestId('target-port')).toBeVisible()
    await expect(dialog.getByTestId('bind-addr')).toBeVisible()
    await expect(dialog.getByTestId('bind-port')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-create')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-cancel')).toBeVisible()
  })

  test('add tunnel dialog validates required fields', async ({ page, workspace }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()
    await dialog.getByTestId('target-port').fill('3000')
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
  })

  test('tunnel creation adds tunnel to sidebar', async ({ page, workspace }) => {
    const { workersSection, dialog } = await openAddTunnelDialog(page)

    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:3000/)).toBeVisible()
  })

  test('tunnel creation error is displayed in dialog', async ({ page, workspace }) => {
    // Override CreateTunnel to reject with an error.
    await page.evaluate(() => {
      ;(window as any).go.main.App.CreateTunnel = () => {
        return Promise.reject(new Error('bind 127.0.0.1:3000: address already in use'))
      }
    })

    const { dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog.getByText('address already in use')).toBeVisible()
    // Dialog should remain open so the user can retry.
    await expect(dialog).toBeVisible()
  })

  test('cancel button closes dialog without creating tunnel', async ({ page, workspace }) => {
    const { workersSection, dialog } = await openAddTunnelDialog(page)

    await dialog.getByTestId('tunnel-cancel').click()
    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:\d+\s*\u2192/)).not.toBeVisible()
  })
})
