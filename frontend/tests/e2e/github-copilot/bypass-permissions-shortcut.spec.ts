import { copilotTest } from '../copilot-fixtures'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { exerciseCopilotPermissionPreset, exerciseCopilotPresetSwitch } from './permissionScenario'

copilotTest('bypass-permissions-shortcut: permission presets switch the native permission mode', async ({ native }) => {
  await exerciseCopilotPresetSwitch(native.page)
})

copilotTest('runs a real native write through the bypass shortcut', async ({ native }) => {
  await exerciseCopilotPermissionPreset(native, 'bypass')
})

copilotTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})
