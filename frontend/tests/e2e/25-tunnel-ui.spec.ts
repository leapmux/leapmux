import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// The tunnel feature requires desktop capabilities, which aren't available in
// normal browser E2E runs. These tests inject a mock desktop bridge before page
// load so the UI renders the tunnel entrypoints at initial render time.

/**
 * Add a page.addInitScript that sets up a mock Tauri desktop environment with
 * tunnel methods only. Hub transport remains browser-native in the test page.
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

    // Per-command overrides set later via page.evaluate(). When present,
    // the override runs instead of the default handler — this lets
    // individual tests inject failure modes without redefining the
    // entire mock.
    const overrides: Record<string, (args?: any) => Promise<any>> = {}
    ;(window as any).__TUNNEL_MOCK_OVERRIDES__ = overrides

    const handleInvoke = (cmd: string, args?: any) => {
      const override = overrides[cmd]
      if (override)
        return override(args)
      switch (cmd) {
        case 'create_tunnel': {
          const config = args.config
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
        }
        case 'delete_tunnel': {
          const idx = tunnels.findIndex(t => t.id === args.tunnelId)
          if (idx >= 0)
            tunnels.splice(idx, 1)
          return Promise.resolve()
        }
        case 'list_tunnels':
          return Promise.resolve([...tunnels])
        case 'get_runtime_state':
          return Promise.resolve({
            shellMode: 'distributed',
            connected: true,
            hubUrl: window.location.origin,
            capabilities: {
              mode: 'tauri-desktop-distributed',
              hubTransport: 'direct',
              tunnels: true,
              appControl: true,
              windowControl: true,
              systemPermissions: true,
              localSolo: false,
            },
          })
        default:
          return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      }
    }

    // Tauri 2's @tauri-apps/api/core invoke routes through
    // __TAURI_INTERNALS__.invoke. The legacy __TAURI_INVOKE__ entry is
    // kept for any code path that still references it.
    ;(window as any).__TAURI_INTERNALS__ = { invoke: handleInvoke }
    ;(window as any).__TAURI_INVOKE__ = handleInvoke
  })
}

/** Open the worker context menu in the Workers sidebar section. */
async function openWorkerMenu(page: import('@playwright/test').Page) {
  const workersSection = page.getByTestId('section-header-workers')
  await expect(workersSection).toBeVisible()
  const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
  if (!isOpen)
    await workersSection.locator('> [role="button"]').click()

  await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()

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
  // Inject the desktop capability mock before navigation so the tunnel UI is
  // available on first render.
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
      ;(window as any).__TUNNEL_MOCK_OVERRIDES__.create_tunnel = () =>
        Promise.reject(new Error('bind 127.0.0.1:3000: address already in use'))
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
