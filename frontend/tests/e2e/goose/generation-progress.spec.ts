import { gooseTest } from '../goose-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'
import { bypassToolRequests } from './scenarios'

gooseTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, prepare: () => bypassToolRequests(native) })
})
