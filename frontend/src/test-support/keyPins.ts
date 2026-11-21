import type { WorkerKeyPinRecord } from '~/lib/browserStorage'
import type { WorkerKeyBundle } from '~/lib/workerKeyBundle'
import { MLKEM_PUBLIC_KEY_SIZE, SLHDSA_PUBLIC_KEY_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import { updateKeyPinState } from '~/lib/browserStorage'
import { readWorkerKeyPin } from '~/lib/keyPinStore'

/** A complete composite key for storage tests that do not perform a handshake. */
export function testKeyPinHex(byte: string): string {
  return byte.repeat(X25519_PUBLIC_KEY_SIZE + MLKEM_PUBLIC_KEY_SIZE + SLHDSA_PUBLIC_KEY_SIZE)
}

/** Full-width keys for channel tests that replace the handshake. */
export function testWorkerKeyBundle(fill = 0): WorkerKeyBundle {
  return {
    x25519PublicKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE).fill(fill),
    mlkemPublicKey: new Uint8Array(MLKEM_PUBLIC_KEY_SIZE).fill(fill),
    slhdsaPublicKey: new Uint8Array(SLHDSA_PUBLIC_KEY_SIZE).fill(fill),
  }
}

/**
 * Seed (or replace) one worker's stored key pin through the pin gateway.
 *
 * The gateway is the only writer of the pin rows, and its `record` change
 * validates the expectation a decision carries. A seed has no earlier
 * decision, so it reads the current state first and passes exactly what it
 * observed -- the same pair a `resolve` commit would carry. `firstSeen` is a
 * parameter on purpose: ordering assertions need values that do not depend on
 * the wall clock.
 */
export async function seedKeyPin(workerId: string, pin: WorkerKeyPinRecord): Promise<void> {
  const { pin: expectPin, removalRevision: expectRevision } = await readWorkerKeyPin(workerId)
  await updateKeyPinState({
    kind: 'record',
    workerId,
    pin,
    expectRevision,
    expectPin,
  })
}
