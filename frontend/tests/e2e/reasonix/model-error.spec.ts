import { randomUUID } from 'node:crypto'
import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('shows the native model failure and accepts a later valid prompt', async ({ native }) => {
  // Reasonix 1.38.7 masks each credential-shaped token of a provider error as
  // `****` before the error reaches its client. Its patterns include 32 or more
  // hexadecimal digits and 40 or more base64 characters in a row. The usual
  // marker (`NATIVEERROR` and 32 letters, see `nativeErrorMarker`) is one run of
  // 43 characters, so it arrives as `****`. Each word of this marker has at most
  // 12 characters, so the error keeps it.
  const message = `NATIVEERROR ${randomUUID().replaceAll('-', ' ')}`
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    attempts: 2,
    error: { status: 400, code: 'invalid_request_error', message },
  })
})

reasonixTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // Reasonix 2.53.1 sends a request whose stream ends after a partial answer,
  // with no completion, six times in about 18 s. It then fails the prompt with
  // "stream ended before completion". It sends a 5xx again every 15 s or so for
  // minutes, which exceeds the test budget, so this case cuts the stream instead.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The stream ended before its completion.',
      midStream: 'truncate',
    },
    attempts: 6,
  })
})
