import { exerciseModelError, exerciseTransientModelFailure, nativeErrorMarker } from '../helpers/nativeModelError'
import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { qoderTest } from '../qoder-fixtures'

qoderTest('shows the native model error and runs a later valid turn', async ({ native }) => {
  await exerciseModelError(native, {
    queueAfterFailure: 'running',
    // qodercli 1.1.65 replaces the message of an HTTP status error with its own
    // text ("The request could not be completed. Please try again."), so that
    // message never reaches the transcript. Qoder relays the provider's own
    // words only for a stream that fails after a partial answer, in the
    // `errors` of its `result`.
    error: {
      status: 500,
      code: 'stream_error',
      message: nativeErrorMarker(),
      midStream: true,
    },
  })
})

qoderTest('folds a retried model error into one resolved row', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      { status: 503, message: 'The service is unavailable.' },
    ],
  })
})

qoderTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // qodercli 1.1.78 sends a request eleven times. Without a `retry-after` header
  // its backoff starts at 0.5 s and doubles to 32 s, which exceeds the test
  // budget. It waits as long as the header states instead, so each error states
  // one second. It then ends the turn with `error_code` 10500.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The server had an error while processing your request.',
      retryAfterSeconds: 1,
    },
    attempts: 11,
  })
})
