import { droidTest } from '../droid-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

droidTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

// No case here checks LeapMux's own server-error retry for Droid, because no
// failure that Droid 0.249.0 gives up on fits the test budget:
// - It retries a 5xx with a backoff that grows from 5 s to 35 s. The seventh
//   503 came 107 s after the prompt, and the turn was still open.
// - A `retry-after` header of one second does not shorten that backoff.
// - A stream that ends after a partial answer ends the turn as a success.
