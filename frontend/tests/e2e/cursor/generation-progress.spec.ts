import { cursorTest } from '../cursor-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

cursorTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
