import { gooseTest } from '../goose-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
