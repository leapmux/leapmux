import type { Page } from '@playwright/test'
import { accountStorageKey, KEY_KEY_PINS } from '../../src/lib/browserStorage'
import { expect, test } from './fixtures'
import { readEntry, writeEntry } from './helpers/storage'
import { loginViaToken, openWorkspace, waitForWorkspaceReady } from './helpers/ui'

/**
 * The stored key-pins key for the signed-in account.
 *
 * Composed by `browserStorage`'s own builder rather than written out: the
 * layout is that module's to state, and a literal here is what went stale the
 * moment the layout changed.
 */
function keyPinsStorageKey(userId: string): string {
  return accountStorageKey(userId, KEY_KEY_PINS)
}

/** One pinned worker key, as the store holds it. */
interface StoredKeyPin {
  publicKeyHex: string
  firstSeen: number
}

/** One worker's key pin, out of the consolidated key-pins map. */
async function getKeyPin(page: Page, userId: string, workerId: string): Promise<StoredKeyPin | null> {
  const row = await readEntry(page, keyPinsStorageKey(userId))
  const pins = row?.v as Record<string, StoredKeyPin> | undefined
  return pins?.[workerId] ?? null
}

/** Replace one pin, preserving the row's expiration. */
async function replaceKeyPin(page: Page, userId: string, workerId: string, publicKeyHex: string) {
  const storedKey = keyPinsStorageKey(userId)
  const row = await readEntry(page, storedKey)
  if (row === null)
    throw new Error('key-pin storage was not initialized')
  const pins = { ...row.v as Record<string, unknown> }
  pins[workerId] = { publicKeyHex, firstSeen: Date.now() - 86400000 }
  await writeEntry(page, storedKey, pins, row.e)
}

test.describe('Key Pinning', () => {
  test('first connection pins the worker public key in browser storage', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    // Verify the key was pinned in this account's consolidated key-pins map.
    await expect.poll(() => getKeyPin(page, adminUserId, workerId)).not.toBeNull()
    const pin = await getKeyPin(page, adminUserId, workerId)

    expect(pin).not.toBeNull()
    expect(pin!.publicKeyHex).toBeTruthy()
    expect(typeof pin!.publicKeyHex).toBe('string')
    // Composite key: X25519 (32) + ML-KEM-1024 (1568) + SLH-DSA (64) = 1664 bytes = 3328 hex chars
    expect(pin!.publicKeyHex.length).toBe(3328)
    expect(pin!.firstSeen).toBeGreaterThan(0)
  })

  test('accept: key mismatch dialog appears, user accepts, workspace loads', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    // Verify key is pinned.
    await expect.poll(() => getKeyPin(page, adminUserId, workerId)).not.toBeNull()
    const pin = await getKeyPin(page, adminUserId, workerId)
    expect(pin).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await replaceKeyPin(page, adminUserId, workerId, 'aa'.repeat(32))

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
    //
    // Polled, not read once: accepting the dialog only RESOLVES the decision.
    // The store hands the caller a `commit` closure that runs when the channel
    // handshake it was blocking actually completes, so the new pin lands some
    // time after the dialog dismisses and after the workspace shell renders. A
    // single read here saw the tampered key every time.
    await expect
      .poll(async () => (await getKeyPin(page, adminUserId, workerId))?.publicKeyHex)
      .not
      .toBe('aa'.repeat(32))
    const updatedPin = await getKeyPin(page, adminUserId, workerId)
    expect(updatedPin).not.toBeNull()
    // Composite key: X25519 (32) + ML-KEM-1024 (1568) + SLH-DSA (64) = 1664 bytes = 3328 hex chars
    expect(updatedPin!.publicKeyHex.length).toBe(3328)
  })

  test('reject: key mismatch dialog appears, user rejects, channel not opened', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    await expect.poll(() => getKeyPin(page, adminUserId, workerId)).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await replaceKeyPin(page, adminUserId, workerId, 'bb'.repeat(32))

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
    const unchangedPin = await getKeyPin(page, adminUserId, workerId)
    expect(unchangedPin).not.toBeNull()
    expect(unchangedPin!.publicKeyHex).toBe('bb'.repeat(32))
  })
})
