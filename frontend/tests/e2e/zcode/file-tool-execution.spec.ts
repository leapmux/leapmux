import { exerciseFileEditSequence, exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { zcodeReadRangeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('renders an applied file edit', async ({ authenticatedZCodeWorkspace, native }) => {
  // Build mode asks before a write.
  // Select Yolo so the scripted write completes and the case can inspect its applied diff.
  await applyPermissionPreset(native.page, 'bypass')
  await expectSettingsChip(native.page, 'Yolo')
  // ZCode requires an earlier Read before Edit.
  // It otherwise returns "File has not been read yet. Read it first before writing to it."
  // The sequence seeds the file through the shell, reads it, and then edits it, so it meets that native precondition.
  await exerciseFileEditSequence(native, { workingDir: authenticatedZCodeWorkspace.workingDir, fileName: 'parity.ts' })
})

zcodeTest('reads and changes actual scratch bytes through native file tools', async ({ native }) => {
  await exerciseFileToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass'), readAfterCall: (id, path) => zcodeReadRangeToolCall(id, path, { offset: 1, limit: 1 }) })
})
