import { join } from 'node:path'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { writeKiroProjectMcpServers } from './mcpConfiguration'
import { nativeContext } from './scenarios'

kiroTest('runs the actual project MCP tool and receives its native service result', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const directory = createTestDirectory('kiro-mcp-echo-')
  const receiptLog = join(directory, 'native-mcp-receipt.json')
  const server = writeMcpEchoServer(directory, { receiptLog })
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, {
    optionValues: { policyPreset: 'allow-all' },
    prepare: (workingDir) => {
      writeKiroProjectMcpServers(workingDir, server)
    },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  // Kiro loads the server in the background. Its tool exists once Kiro lists it.
  await waitForMcpToolListed(receiptLog, 'echo')
  // The permissive preset runs the tool without a request, so the call must raise no banner.
  await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: () => exerciseMcpEcho(context, 'KIRO_NATIVE_ECHO_VALUE', { receiptLog }) })
})
