import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

const KEY_PINS_STORAGE_KEY = 'leapmux:key-pins'

/** Read a worker's key pin from the consolidated key-pins map. */
async function getKeyPin(page: Page, workerId: string) {
  return page.evaluate(([key, wid]) => {
    const raw = localStorage.getItem(key)
    if (!raw)
      return null
    const pins = JSON.parse(raw).v
    return pins[wid] ?? null
  }, [KEY_PINS_STORAGE_KEY, workerId] as const)
}

/** Replace one pin while preserving the browser-storage expiry envelope. */
async function replaceKeyPin(page: Page, workerId: string, publicKeyHex: string) {
  await page.evaluate(([key, wid, replacement]) => {
    const raw = localStorage.getItem(key)
    if (!raw)
      throw new Error('key-pin storage was not initialized')
    const wrapped = JSON.parse(raw)
    wrapped.v[wid] = {
      publicKeyHex: replacement,
      firstSeen: Date.now() - 86400000,
    }
    localStorage.setItem(key, JSON.stringify(wrapped))
  }, [KEY_PINS_STORAGE_KEY, workerId, publicKeyHex] as const)
}

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
    await expect.poll(() => getKeyPin(page, workerId)).not.toBeNull()
    const pin = await getKeyPin(page, workerId)

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
    await expect.poll(() => getKeyPin(page, workerId)).not.toBeNull()
    const pin = await getKeyPin(page, workerId)
    expect(pin).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await replaceKeyPin(page, workerId, 'aa'.repeat(32))

    // Reload the page to destroy the in-memory ChannelManager and force a new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible()

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
    const updatedPin = await getKeyPin(page, workerId)
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

    await expect.poll(() => getKeyPin(page, workerId)).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await replaceKeyPin(page, workerId, 'bb'.repeat(32))

    // Reload to trigger new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible()

    // Click "Reject".
    await page.locator('[data-testid="key-pin-reject"]').click()

    // Dialog should dismiss.
    await expect(dialog).not.toBeVisible()

    // The pin should NOT be updated (still the fake key).
    const unchangedPin = await getKeyPin(page, workerId)
    expect(unchangedPin).not.toBeNull()
    expect(unchangedPin.publicKeyHex).toBe('bb'.repeat(32))
  })
})
