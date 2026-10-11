import { exerciseTokenProgress } from '../helpers/generationProgress'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, prepare: () => bypassToolRequests(native) })
})
