import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { droidTest } from '../droid-fixtures'
import { exerciseNativeWorkspaceTrustLimit, mcpServerProjectConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { nativeContext } from './scenarios'

droidTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: mcpServerProjectConfiguration((directory, server) => {
      const configuration = join(directory, '.factory', 'mcp.json')
      mkdirSync(dirname(configuration), { recursive: true })
      writeFileSync(configuration, JSON.stringify({ mcpServers: { [server.name]: { command: server.command, args: server.args } } }))
    }),
  })
})
