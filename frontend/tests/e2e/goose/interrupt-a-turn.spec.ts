import { gooseTest } from '../goose-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('stops a native turn and accepts a new prompt after queue resume', async ({ native }) => {
  // Goose 1.53.0 ends a turn on session/cancel only after the response stream
  // starts, so the turn holds after the first streamed chunk.
  await exerciseInterruptTurn(native, { prepare: () => applyPermissionPreset(native.page, 'bypass'), holdModelTurn: 'after-first-chunk' })
})
