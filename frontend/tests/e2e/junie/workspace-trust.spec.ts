import { exerciseNativeWorkspaceTrustLimit, mcpServerProjectConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { junieTest } from '../junie-fixtures'
import { writeJunieMcpConfig } from './mcpConfig'
import { nativeContext } from './scenarios'

junieTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: mcpServerProjectConfiguration((directory, server) => {
      writeJunieMcpConfig(directory, server.name, server.command, server.args)
    }),
  })
})
