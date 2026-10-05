import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileEditSequence, exerciseFileToolExecution, exerciseFileWriteSequence } from '../helpers/nativeToolExecution'
import { waitForControlBanner } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI file tool execution', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.QODER

  qoderTest('seeds, reads and edits a file, and draws the edit diff', async ({ qoderWorkspace, page, modelScript }) => {
    const fileName = 'qoder-file-probe.txt'
    await exerciseFileEditSequence({ page, modelScript, provider: PROVIDER }, {
      workingDir: qoderWorkspace.workingDir,
      fileName,
      approveSeed: async (firstStepCount) => {
        await modelScript.waitForSteps(firstStepCount)
        const banner = await waitForControlBanner(page)
        await expect(banner).toContainText(fileName)
        await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
      },
    })
  })

  qoderTest('writes a new file and lands its content on disk', async ({ qoderWorkspace, page, modelScript }) => {
    await exerciseFileWriteSequence({ page, modelScript, provider: PROVIDER }, {
      workingDir: qoderWorkspace.workingDir,
      fileName: 'qoder-written-probe.txt',
    })
  })
})

qoderTest('reads and changes native files and keeps the applied diff after reload', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})
