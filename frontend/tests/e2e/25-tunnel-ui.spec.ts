import { expect, test } from './fixtures'

// The tunnel feature requires the desktop app (Wails), which isn't available
// in E2E tests. These tests inject a mock __lm_call to verify the UI renders
// correctly when the tunnel API is available.

async function injectTunnelMock(page: import('@playwright/test').Page) {
  await page.evaluate(() => {
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

    ;(window as any).__lm_call = (method: string, args: unknown[]) => {
      if (method === 'main.App.CreateTunnel') {
        const config = args[0] as any
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
      if (method === 'main.App.DeleteTunnel') {
        const id = args[0] as string
        const idx = tunnels.findIndex(t => t.id === id)
        if (idx >= 0)
          tunnels.splice(idx, 1)
        return Promise.resolve()
      }
      if (method === 'main.App.ListTunnels') {
        return Promise.resolve([...tunnels])
      }
      return Promise.reject(new Error(`unknown method: ${method}`))
    }
  })
}

test.describe('Tunnel UI', () => {
  test('add tunnel menu item appears when tunnel API is available', async ({ page, authenticatedWorkspace }) => {
    await injectTunnelMock(page)

    // Open the Workers section.
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    // Wait for the worker to appear.
    await expect(workersSection.getByText('Local')).toBeVisible()

    // Click the three-dot menu on the worker.
    const menuButton = workersSection.locator('[aria-expanded]').first()
    await menuButton.click()

    // "Add tunnel..." should be visible since __lm_call is injected and
    // in solo mode the user is the owner.
    await expect(page.getByRole('menuitem', { name: 'Add tunnel...' })).toBeVisible()
  })

  test('add tunnel dialog opens and has correct fields', async ({ page, authenticatedWorkspace }) => {
    await injectTunnelMock(page)

    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    await expect(workersSection.getByText('Local')).toBeVisible()

    const menuButton = workersSection.locator('[aria-expanded]').first()
    await menuButton.click()
    await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()

    // Dialog should appear.
    const dialog = page.getByTestId('add-tunnel-dialog')
    await expect(dialog).toBeVisible()

    // Port forwarding should be selected by default.
    await expect(dialog.getByTestId('target-addr')).toBeVisible()
    await expect(dialog.getByTestId('target-port')).toBeVisible()
    await expect(dialog.getByTestId('bind-addr')).toBeVisible()
    await expect(dialog.getByTestId('bind-port')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-create')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-cancel')).toBeVisible()
  })

  test('add tunnel dialog validates required fields', async ({ page, authenticatedWorkspace }) => {
    await injectTunnelMock(page)

    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    await expect(workersSection.getByText('Local')).toBeVisible()

    const menuButton = workersSection.locator('[aria-expanded]').first()
    await menuButton.click()
    await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()

    const dialog = page.getByTestId('add-tunnel-dialog')
    await expect(dialog).toBeVisible()

    // Create button should be disabled when target port is empty.
    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()

    // Fill target port.
    await dialog.getByTestId('target-port').fill('3000')
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
  })

  test('tunnel creation adds tunnel to sidebar', async ({ page, authenticatedWorkspace }) => {
    await injectTunnelMock(page)

    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    await expect(workersSection.getByText('Local')).toBeVisible()

    const menuButton = workersSection.locator('[aria-expanded]').first()
    await menuButton.click()
    await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()

    const dialog = page.getByTestId('add-tunnel-dialog')
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    // Dialog should close.
    await expect(dialog).not.toBeVisible()

    // Tunnel should appear in the sidebar under the worker.
    await expect(workersSection.getByText(/127\.0\.0\.1:3000/)).toBeVisible()
  })

  test('cancel button closes dialog without creating tunnel', async ({ page, authenticatedWorkspace }) => {
    await injectTunnelMock(page)

    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    await expect(workersSection.getByText('Local')).toBeVisible()

    const menuButton = workersSection.locator('[aria-expanded]').first()
    await menuButton.click()
    await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()

    const dialog = page.getByTestId('add-tunnel-dialog')
    await expect(dialog).toBeVisible()

    await dialog.getByTestId('tunnel-cancel').click()
    await expect(dialog).not.toBeVisible()

    // No tunnel should appear.
    await expect(workersSection.getByText(/127\.0\.0\.1:\d+\s*\u2192/)).not.toBeVisible()
  })
})
