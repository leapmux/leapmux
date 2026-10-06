import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { qoderTest } from '../qoder-fixtures'

qoderTest('exposes no token or byte counter throughout the completed native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: false })
})

qoderTest('reports no byte count throughout an actual native shell output stream', async ({ native }) => {
  await exerciseOutputByteProgress(native, { supported: false, approveTool: true })
})
