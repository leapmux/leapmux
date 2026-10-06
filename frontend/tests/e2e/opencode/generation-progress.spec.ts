import { exerciseTokenProgress } from '../helpers/generationProgress'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
