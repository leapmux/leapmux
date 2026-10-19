import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'
import { qoderTest } from '../qoder-fixtures'

qoderTest('shows token progress throughout the native stream', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})

qoderTest('reports no byte count throughout the native shell output stream', async ({ native }) => {
  // Qoder forwards model deltas, but its native stream supplies no incremental tool output.
  await exerciseOutputByteProgress(native, { supported: false, approveTool: true })
})
