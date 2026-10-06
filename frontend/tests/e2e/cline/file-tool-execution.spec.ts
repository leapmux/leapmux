import { clineTest } from '../cline-fixtures'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { exerciseNativeFileEdit, exerciseNativeFileRead, exerciseNativeFileWrite } from '../helpers/nativeToolExecution'

/**
 * The installed agent runs its native file tools on real files:
 *
 * - Write.
 * - Edit.
 * - Read.
 *
 * The transcript must show the native read and the edit diff.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline tool execution', () => {
  clineTest('draws the lines a read returns', async ({ authenticatedClineWorkspace, native }) => {
    // Cline numbers each line `<n> | ` for the model.
    await exerciseNativeFileRead(native, { directory: createNativeToolDirectory(authenticatedClineWorkspace.workingDir), linePrefix: 'cline-read', numberedLine: (line, text) => `${line} | ${text}` })
  })

  clineTest('draws the diff of an edit', async ({ authenticatedClineWorkspace, native }) => {
    await exerciseNativeFileEdit(native, { directory: createNativeToolDirectory(authenticatedClineWorkspace.workingDir) })
  })

  clineTest('creates the file that a write call states', async ({ authenticatedClineWorkspace, native }) => {
    // No run has pinned whether Cline's editor ends a created file with a newline, so the pattern admits that one
    // difference and no other text.
    await exerciseNativeFileWrite(native, { directory: createNativeToolDirectory(authenticatedClineWorkspace.workingDir), content: 'cline was here', stored: /^cline was here\n?$/ })
  })
})
