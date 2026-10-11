import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { expect } from '@playwright/test'

/**
 * Wait until `read` finds a value in a recorded model request, and return the value of the newest such request.
 *
 * `read` returns null for a request that does not carry the value. A `read` that throws ends the wait at once with
 * its error: a recorded request does not change, so a later read refuses it again.
 */
export async function waitForNewestModelRequest<T>(
  modelScript: Pick<ModelScript, 'status'>,
  read: (request: MockModelRequestRecord) => T | null,
): Promise<T> {
  let found: { value: T } | undefined
  await expect.poll(async () => {
    for (const request of [...(await modelScript.status()).requests].reverse()) {
      const value = read(request)
      if (value !== null) {
        found = { value }
        return true
      }
    }
    return false
  }).toBe(true)
  if (!found)
    throw new Error('The model request wait ended with no value.')
  return found.value
}
