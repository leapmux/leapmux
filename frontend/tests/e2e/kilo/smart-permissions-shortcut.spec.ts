import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'

kiloTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseRelatedTodo(native) })
})
