import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseFileEditSequence, exerciseFileToolExecution, exerciseFileWriteSequence } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code file tool execution', () => {
  const PROVIDER = AgentProvider.CODEBUDDY

  codebuddyTest('seeds, reads and edits a file, and draws the edit diff', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    await exerciseFileEditSequence({ page, modelScript, provider: PROVIDER }, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      fileName: 'codebuddy-file-probe.txt',
    })
  })

  codebuddyTest('writes a new file and lands its content on disk', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    await exerciseFileWriteSequence({ page, modelScript, provider: PROVIDER }, {
      workingDir: authenticatedCodebuddyWorkspace.workingDir,
      fileName: 'codebuddy-written-probe.txt',
    })
  })
})

codebuddyTest('reads and changes native files and keeps the applied diff after reload', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})
