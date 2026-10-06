import { exerciseTokenProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('reports advancing native generation counts and keeps completed content', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
