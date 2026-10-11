import { exerciseTokenProgress } from '../helpers/generationProgress'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

zcodeTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, prepare: () => bypassToolRequests(native) })
})
