import { copilotTest } from '../copilot-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
