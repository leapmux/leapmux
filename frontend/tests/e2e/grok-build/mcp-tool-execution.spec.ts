import { grokTest } from '../grok-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { openGrokMcpWorkspace } from './mcpWorkspace'
import { nativeContext } from './scenarios'

grokTest('runs the actual registered MCP tool and reads only its native result', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const { receiptLog } = await openGrokMcpWorkspace(context, 'allow')
  // The permissive preset runs the tool without a request, so the call must raise no banner.
  await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: () => exerciseMcpEcho(context, 'GROK_NATIVE_ECHO_VALUE', { receiptLog }) })
})
