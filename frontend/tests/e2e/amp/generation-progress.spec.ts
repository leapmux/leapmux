import { ampTest } from '../amp-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'

ampTest('proves the native stream supplies no live counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

ampTest('proves real native shell output supplies no live byte counter', async ({ native }) => {
  await exerciseOutputByteProgress(native, { supported: false })
})
