import { exerciseFileEditSequence, exerciseFileToolExecution, exerciseFileWriteSequence } from '../helpers/nativeToolExecution'
import { answerControl, waitForControlBanner } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI file tool execution', () => {
  qoderTest('seeds, reads and edits a file, and draws the edit diff', async ({ authenticatedQoderWorkspace, native }) => {
    const fileName = 'qoder-file-probe.txt'
    await exerciseFileEditSequence(native, {
      workingDir: authenticatedQoderWorkspace.workingDir,
      fileName,
      approveSeed: async (firstStepCount) => {
        await native.modelScript.waitForSteps(firstStepCount)
        const banner = await waitForControlBanner(native.page)
        await expect(banner).toContainText(fileName)
        await answerControl(native.page, 'allow')
      },
    })
  })

  qoderTest('writes a new file and lands its content on disk', async ({ authenticatedQoderWorkspace, native }) => {
    await exerciseFileWriteSequence(native, {
      workingDir: authenticatedQoderWorkspace.workingDir,
      fileName: 'qoder-written-probe.txt',
    })
  })
})

qoderTest('reads and changes native files and keeps the applied diff after reload', async ({ native }) => {
  await exerciseFileToolExecution(native)
})
