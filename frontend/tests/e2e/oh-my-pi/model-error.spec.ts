import { exerciseModelError, exerciseTransientModelFailure } from '../helpers/nativeModelError'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('shows the native model failure and accepts the next valid prompt', async ({ native }) => {
  await exerciseModelError(native, { queueAfterFailure: 'running' })
})

ohMyPiTest('schedules LeapMux\'s own server-error retry after the native client gives up', async ({ native }) => {
  // omp 18.12.6 sends a 5xx six times in each round of its HTTP client, and
  // runs round after round: 28 requests took 100 s, which exceeds the test
  // budget. It sends a stream that ends after a partial answer once, and ends
  // the turn with a transient failure, so this case cuts the stream instead.
  await exerciseTransientModelFailure(native, {
    error: {
      status: 500,
      code: 'server_error',
      message: 'The stream ended before its completion.',
      midStream: 'truncate',
    },
    attempts: 1,
  })
})
