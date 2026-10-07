import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  // omp appends a `Wall time: <n> seconds` notice to every output, and `Command exited with code N` to the output of a
  // failed command. The extractor drops both, because the row already states how the call ended.
  await exerciseShellToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass'), absentRowText: ['Wall time', 'Command exited with code'] })
})
