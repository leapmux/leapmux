import { expect } from '@playwright/test'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { applyPermissionPreset, chatText } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
  // omp appends a `Wall time: <n> seconds` notice to every output. The
  // extractor drops it, because the row already states how the call ended.
  // The scenario requires the row of the command output, so the chat holds
  // the output that this check reads.
  expect(await chatText(native.page)).not.toContain('Wall time:')
})
