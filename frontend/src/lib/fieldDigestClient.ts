import type { FieldDigestRequest, FieldDigestResponse } from './fieldDigestWorker'
import { ownedBytes } from './bytes'
import { digestFieldsInSlices } from './fieldDigest'
import { createWorkerClient } from './workerClient'

// ---------------------------------------------------------------------------
// Field digest worker client
//
// Hashes a list of fields in the hash worker, so that the main thread pays only
// for the copy of the bytes: about 0.4 ms per MiB, against about 18 ms per MiB
// for the hash itself. When the worker fails, the main thread computes the same
// digest in work slices (see digestFieldsInSlices), so a retry still finds its
// attempt.
// ---------------------------------------------------------------------------

/** The time budget of every request, whatever its size. It covers the load of the worker. */
export const FIELD_DIGEST_BASE_BUDGET_MS = 2_000

/**
 * The budget added for each MiB that a request starts: 13 times the 18 ms per
 * MiB that a desktop takes, which leaves room for a phone.
 */
export const FIELD_DIGEST_BUDGET_MS_PER_MIB = 250

const BYTES_PER_MIB = 1024 * 1024

/**
 * The longest the worker may take to hash `totalBytes`, in milliseconds (see
 * WorkerRequestOptions). A send waits on the digest, so a module fetch that
 * stalls must not hold it for long. A budget that runs out still gives the right
 * digest, through the sliced hash on the main thread.
 */
export function fieldDigestBudgetMs(totalBytes: number): number {
  return FIELD_DIGEST_BASE_BUDGET_MS + Math.ceil(totalBytes / BYTES_PER_MIB) * FIELD_DIGEST_BUDGET_MS_PER_MIB
}

// `undefined` is the client's failure value: the worker could not start, it
// crashed, or it ran past its budget. The budget covers the worker's load too, so
// the client takes no cold-start allowance.
const digestClient = createWorkerClient<FieldDigestRequest, string | undefined>({
  spawn: () => new Worker(new URL('./fieldDigestWorker.ts', import.meta.url), { type: 'module' }),
  extract: (data: FieldDigestResponse) => ({ id: data.id, value: data.digest }),
  failureValue: undefined,
  coldStartAllowanceMs: 0,
})

/**
 * The digest of `fields` (see digestFields), computed in the hash worker, or on
 * this thread in work slices when the worker fails. Never rejects.
 *
 * The worker receives a copy of each field, never a transfer: the caller still
 * needs the bytes, and the client posts a request again to a fresh worker after
 * a crash.
 */
export async function digestFieldsOffThread(fields: readonly Uint8Array[]): Promise<string> {
  let totalBytes = 0
  for (const field of fields)
    totalBytes += field.byteLength
  const digest = await digestClient.request(
    id => ({
      id,
      fields: fields.map(field => ownedBytes(field)),
    }),
    {
      timeBudgetMs: fieldDigestBudgetMs(totalBytes),
    },
  )
  return digest ?? digestFieldsInSlices(fields)
}
