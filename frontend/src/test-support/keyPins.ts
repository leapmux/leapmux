import type { WorkerKeyPinRecord } from '~/lib/browserStorage'
import { readKeyPinState, updateKeyPinState } from '~/lib/browserStorage'

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
  const state = await readKeyPinState()
  await updateKeyPinState({
    kind: 'record',
    workerId,
    pin,
    expectRevision: state.removalRevision,
    expectPin: state.pins[workerId] ?? null,
  })
}
