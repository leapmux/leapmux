import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseFileEditSequence, exerciseFileToolExecution, exerciseFileWriteSequence } from '../helpers/nativeToolExecution'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  codebuddyTest('seeds, reads and edits a file, and draws the edit diff', async ({ authenticatedCodebuddyWorkspace, native }) => {
    await exerciseFileEditSequence(native, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      fileName: 'codebuddy-file-probe.txt',
    })
  })

  codebuddyTest('writes a new file and lands its content on disk', async ({ authenticatedCodebuddyWorkspace, native }) => {
    await exerciseFileWriteSequence(native, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      fileName: 'codebuddy-written-probe.txt',
    })
  })
})

codebuddyTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
