/**
 * The pause that the mock takes between two pieces of a streamed answer.
 *
 * Every surface of the mock that streams text pauses the same way, so the model
 * routes and the Kiro surface (`./kiroSurface`) share this one pause.
 */
import type { ServerResponse } from 'node:http'

/**
 * Pause between two text pieces, and stop early when the client goes away.
 *
 * An interrupt aborts the request in the middle of an answer, which is the case
 * that this serves. The remaining pieces must not keep a closed socket open for the
 * rest of the delay that the script states.
 */
export function pauseBetweenChunks(response: ServerResponse, delayMs: number): Promise<void> {
  if (delayMs <= 0)
    return Promise.resolve()
  return new Promise<void>((resolve) => {
    const timer = setTimeout(done, delayMs)
    function done(): void {
      clearTimeout(timer)
      response.off('close', done)
      resolve()
    }
    response.once('close', done)
  })
}
