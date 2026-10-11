import { exerciseTokenProgress } from '../helpers/generationProgress'
import { piTest } from '../pi-fixtures'

piTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
