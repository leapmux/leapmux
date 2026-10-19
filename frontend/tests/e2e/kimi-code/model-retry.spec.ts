import { kimiTest } from '../kimi-fixtures'
import { exerciseKimiModelRetry } from './scenarios'

kimiTest('keeps the interrupted attempt and the retried answer apart, live and after a reload', async ({ native }) => {
  await exerciseKimiModelRetry(native)
})
