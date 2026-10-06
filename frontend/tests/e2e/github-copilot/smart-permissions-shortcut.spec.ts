import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotPermissionPreset, exerciseCopilotPresetSwitch } from './permissionScenario'

copilotTest('permission presets switch the native permission mode', async ({ native }) => {
  await exerciseCopilotPresetSwitch(native.page)
})

copilotTest('runs a real native write through the smart shortcut', async ({ native }) => {
  await exerciseCopilotPermissionPreset(native, 'smart')
})
