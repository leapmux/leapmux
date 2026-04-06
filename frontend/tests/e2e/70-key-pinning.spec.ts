import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('Key Pinning', () => {
  test('first connection pins the worker public key in localStorage', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    // Verify the key was pinned in the consolidated leapmux:key-pins map.
    const pin = await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      if (!raw)
        return null
      const pins = JSON.parse(raw)
      return pins[wid] ?? null
    }, workerId)

    expect(pin).not.toBeNull()
    expect(pin.publicKeyHex).toBeTruthy()
    expect(typeof pin.publicKeyHex).toBe('string')
    // Composite key: X25519 (32) + ML-KEM-1024 (1568) + SLH-DSA (64) = 1664 bytes = 3328 hex chars
    expect(pin.publicKeyHex.length).toBe(3328)
    expect(pin.firstSeen).toBeGreaterThan(0)
  })

  test('accept: key mismatch dialog appears, user accepts, workspace loads', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    // Verify key is pinned.
    const pin = await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      if (!raw)
        return null
      const pins = JSON.parse(raw)
      return pins[wid] ?? null
    }, workerId)
    expect(pin).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      const pins = raw ? JSON.parse(raw) : {}
      pins[wid] = {
        publicKeyHex: 'aa'.repeat(32), // 64 hex chars of 'aa'
        firstSeen: Date.now() - 86400000,
      }
      localStorage.setItem('leapmux:key-pins', JSON.stringify(pins))
    }, workerId)

    // Reload the page to destroy the in-memory ChannelManager and force a new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible({ timeout: 30_000 })

    // Verify the dialog shows different expected and actual fingerprints.
    const expectedFp = await page.locator('[data-testid="expected-fingerprint"]').textContent()
    const actualFp = await page.locator('[data-testid="actual-fingerprint"]').textContent()
    expect(expectedFp).toBeTruthy()
    expect(actualFp).toBeTruthy()
    expect(expectedFp).not.toBe(actualFp)

    // Click "Accept" (ConfirmButton — requires two clicks).
    const acceptBtn = page.locator('[data-testid="key-pin-accept"]')
    await acceptBtn.click() // First click: arms the button
    await acceptBtn.click() // Second click: confirms

    // Dialog should dismiss.
    await expect(dialog).not.toBeVisible()

    // Workspace should load normally.
    await waitForWorkspaceReady(page)

    // Verify the pin was updated to the real key (not the fake 'aa' key).
    const updatedPin = await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      if (!raw)
        return null
      const pins = JSON.parse(raw)
      return pins[wid] ?? null
    }, workerId)
    expect(updatedPin).not.toBeNull()
    expect(updatedPin.publicKeyHex).not.toBe('aa'.repeat(32))
    // Composite key: X25519 (32) + ML-KEM-1024 (1568) + SLH-DSA (64) = 1664 bytes = 3328 hex chars
    expect(updatedPin.publicKeyHex.length).toBe(3328)
  })

  test('reject: key mismatch dialog appears, user rejects, channel not opened', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      const pins = raw ? JSON.parse(raw) : {}
      pins[wid] = {
        publicKeyHex: 'bb'.repeat(32),
        firstSeen: Date.now() - 86400000,
      }
      localStorage.setItem('leapmux:key-pins', JSON.stringify(pins))
    }, workerId)

    // Reload to trigger new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible({ timeout: 30_000 })

    // Click "Reject".
    await page.locator('[data-testid="key-pin-reject"]').click()

    // Dialog should dismiss.
    await expect(dialog).not.toBeVisible()

    // The pin should NOT be updated (still the fake key).
    const unchangedPin = await page.evaluate((wid) => {
      const raw = localStorage.getItem('leapmux:key-pins')
      if (!raw)
        return null
      const pins = JSON.parse(raw)
      return pins[wid] ?? null
    }, workerId)
    expect(unchangedPin).not.toBeNull()
    expect(unchangedPin.publicKeyHex).toBe('bb'.repeat(32))
  })
})
