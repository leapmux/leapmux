import { clineTest } from '../cline-fixtures'
import { exerciseHeldChildRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset } from '../helpers/ui'

clineTest('keeps the actual native background task row through completion and reload', async ({ page, native: context }) => {
  await applyPermissionPreset(page, 'bypass')
  await exerciseHeldChildRow(context)
})
