import { exerciseTokenProgress } from '../helpers/generationProgress'
import { kiloTest } from '../kilo-fixtures'

kiloTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
