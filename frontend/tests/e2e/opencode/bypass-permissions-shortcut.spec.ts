import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof: () => exerciseRelatedTodo(native) })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/515.
