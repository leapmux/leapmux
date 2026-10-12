import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseNativeWorkspaceTrustLimit, ignoredMcpServerProjectConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { nativeContext } from './scenarios'

fastAgentTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: ignoredMcpServerProjectConfiguration((directory, server) => {
      // A JSON string is a valid YAML scalar, and a JSON array is a valid YAML sequence, so each path stays quoted.
      writeFileSync(join(directory, 'fast-agent.yaml'), `mcp:\n  servers:\n    ${server.name}:\n      command: ${JSON.stringify(server.command)}\n      args: ${JSON.stringify(server.args)}\n`)
    }),
  })
})
