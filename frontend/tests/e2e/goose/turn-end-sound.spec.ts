import { gooseTest } from '../goose-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: updateTodosToolCall(native.provider, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }]), prepare: () => applyPermissionPreset(native.page, 'bypass') })
  await exerciseTurnEndSound(native)
  await exerciseTurnEndSound(native, { sound: 'none', tool: updateTodosToolCall(native.provider, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }]) })
})
