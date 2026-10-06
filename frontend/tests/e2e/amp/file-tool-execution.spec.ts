import { ampTest } from '../amp-fixtures'
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
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws the lines a read returns', async ({ authenticatedAmpWorkspace, native }) => {
    // Amp numbers each line `<n>: ` for the model.
    await exerciseNativeFileRead(native, { directory: createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), linePrefix: 'amp-read', numberedLine: (line, text) => `${line}: ${text}` })
  })

  ampTest('draws the diff of an edit', async ({ authenticatedAmpWorkspace, native }) => {
    await exerciseNativeFileEdit(native, { directory: createNativeToolDirectory(authenticatedAmpWorkspace.workingDir) })
  })

  ampTest('writes the file that a write call states', async ({ authenticatedAmpWorkspace, native }) => {
    // Amp ends the file with a newline.
    await exerciseNativeFileWrite(native, { directory: createNativeToolDirectory(authenticatedAmpWorkspace.workingDir), content: 'amp was here', stored: 'amp was here\n' })
  })
})
