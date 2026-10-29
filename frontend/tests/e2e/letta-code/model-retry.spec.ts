import { exerciseModelRetry } from '../helpers/nativeModelRetry'
import { lettaTest } from '../letta-fixtures'

lettaTest('discards the failed attempt and folds the retry', async ({ native }) => {
  await exerciseModelRetry(native, {
    errors: [
      {
        status: 503,
        midStream: true,
        code: 'server_error',
        message: 'The server had an error. You can retry your request.',
      },
    ],
  })
})
