import { droidTest } from '../droid-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'

droidTest('exposes no token or byte counter throughout the completed native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

droidTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  await exerciseOutputByteProgress(native, { supported: false })
})
