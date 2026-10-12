import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'

fastAgentTest('reports an advancing token count while native output arrives', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})

fastAgentTest('reports an advancing byte count while native output arrives', async ({ native }) => {
  await exerciseOutputByteProgress(native, { supported: true, approveTool: true })
})
