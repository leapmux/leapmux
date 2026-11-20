import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { MLKEM_PUBLIC_KEY_SIZE, SLHDSA_PUBLIC_KEY_SIZE, X25519_PUBLIC_KEY_SIZE } from '../../src/generated/contracts/wire'
import { accountStorageKey, KEY_KEY_PINS, storageAccountFor } from '../../src/lib/browserStorage'
import { test } from './fixtures'
import { readEntry, writeEntry } from './helpers/storage'
import { loginViaToken, openWorkspace, waitForWorkspaceReady } from './helpers/ui'

/**
 * The stored key-pins key for the signed-in account.
 *
 * Composed by `browserStorage`'s own builder rather than written out: the
 * layout is that module's to state, and a literal here is what went stale the
 * moment the layout changed. The account is the (hub, user) pair the app
 * stores under.
 */
function keyPinsStorageKey(account: string): string {
  return accountStorageKey(account, KEY_KEY_PINS)
}

/** One pinned worker key, as the store holds it. */
interface StoredKeyPin {
  publicKeyHex: string
  firstSeen: number
}

/** One worker's key pin, out of the consolidated key-pins map. */
async function getKeyPin(page: Page, account: string, workerId: string): Promise<StoredKeyPin | null> {
  const row = await readEntry(page, keyPinsStorageKey(account))
  const pins = row?.v as Record<string, StoredKeyPin> | undefined
  return pins?.[workerId] ?? null
}

/** Change one hex digit of the verified key, preserving its width and the row's expiration. */
async function tamperKeyPin(page: Page, account: string, workerId: string): Promise<string> {
  const storedKey = keyPinsStorageKey(account)
  const row = await readEntry(page, storedKey)
  if (row === null)
    throw new Error('key-pin storage was not initialized')
  const pins = { ...row.v as Record<string, StoredKeyPin> }
  const existing = pins[workerId]
  if (existing === undefined)
    throw new Error('the Worker has no stored pin to tamper with')
  const firstDigit = existing.publicKeyHex[0] === 'a'
    ? 'b'
    : 'a'
  const publicKeyHex = firstDigit + existing.publicKeyHex.slice(1)
  pins[workerId] = { ...existing, publicKeyHex }
  await writeEntry(page, storedKey, pins, row.e)
  return publicKeyHex
}

test.describe('Key Pinning', () => {
  test('first connection pins the worker public key in browser storage', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(leapmuxServer.hubUrl).origin, adminUserId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    // Verify the key was pinned in this account's consolidated key-pins map.
    await expect.poll(() => getKeyPin(page, account, workerId)).not.toBeNull()
    const pin = await getKeyPin(page, account, workerId)

    expect(pin).not.toBeNull()
    expect(pin!.publicKeyHex).toBeTruthy()
    expect(typeof pin!.publicKeyHex).toBe('string')
    expect(pin!.publicKeyHex).toHaveLength(2 * (X25519_PUBLIC_KEY_SIZE + MLKEM_PUBLIC_KEY_SIZE + SLHDSA_PUBLIC_KEY_SIZE))
    expect(pin!.firstSeen).toBeGreaterThan(0)
  })

  /**
   * The Noise session of every channel runs in the channel session worker: the handshake, the SLH-DSA signature check
   * of a post-quantum open (about 26 ms of JavaScript), encryption and decryption. No main-thread fallback exists, so
   * the channel opens only when that worker runs; this proves that it loads and stays up in the built app.
   */
  test('runs the channel session, and its signature check, in a Web Worker', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(leapmuxServer.hubUrl).origin, adminUserId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)
    await expect.poll(() => getKeyPin(page, account, workerId)).not.toBeNull()

    await expect.poll(() => page.workers().some(worker => worker.url().includes('channelSessionWorker'))).toBe(true)
  })

  test('accept: key mismatch dialog appears, user accepts, workspace loads', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(leapmuxServer.hubUrl).origin, adminUserId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    // Capture the key from the initial verified channel before changing the pin.
    await expect.poll(() => getKeyPin(page, account, workerId)).not.toBeNull()
    const verifiedKeyHex = (await getKeyPin(page, account, workerId))?.publicKeyHex
    expect(verifiedKeyHex).toBeDefined()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    await tamperKeyPin(page, account, workerId)

    // Reload the page to destroy the in-memory ChannelManager and force a new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible()

    // The dialog states WHY a mismatch can only come from outside this worker,
    // and what to check before accepting.
    await expect(dialog).toContainText('A worker keeps its keys for as long as it stays registered')
    await expect(dialog).toContainText('Accept only after you confirm')

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

    // The pin commits after verification, which can complete after the dialog closes.
    await expect.poll(async () => (await getKeyPin(page, account, workerId))?.publicKeyHex).toBe(verifiedKeyHex)
  })

  test('reject: key mismatch dialog appears, user rejects, channel not opened', async ({
    page,
    workspace,
    leapmuxServer,
  }) => {
    const { adminToken, adminUserId, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(leapmuxServer.hubUrl).origin, adminUserId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspace.workspaceId)

    await expect.poll(() => getKeyPin(page, account, workerId)).not.toBeNull()

    // Tamper with the pinned key to trigger a mismatch on next channel open.
    const tamperedKeyHex = await tamperKeyPin(page, account, workerId)

    // Reload to trigger new channel open.
    await page.reload()

    // The key pinning dialog should appear.
    const dialog = page.locator('[data-testid="key-pin-mismatch-dialog"]')
    await expect(dialog).toBeVisible()

    // The dialog states WHY a mismatch can only come from outside this worker,
    // and what to check before accepting.
    await expect(dialog).toContainText('A worker keeps its keys for as long as it stays registered')
    await expect(dialog).toContainText('Accept only after you confirm')

    // Click "Reject".
    await page.locator('[data-testid="key-pin-reject"]').click()

    // Dialog should dismiss.
    await expect(dialog).not.toBeVisible()

    // The pin should NOT be updated (still the fake key).
    const unchangedPin = await getKeyPin(page, account, workerId)
    expect(unchangedPin).not.toBeNull()
    expect(unchangedPin!.publicKeyHex).toBe(tamperedKeyHex)
  })
})
