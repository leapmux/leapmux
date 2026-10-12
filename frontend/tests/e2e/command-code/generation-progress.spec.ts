import { commandCodeTest } from '../command-code-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'

commandCodeTest('counts native model and command bytes and retains the completed result', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
  await exerciseOutputByteProgress(native, { supported: true })
})
