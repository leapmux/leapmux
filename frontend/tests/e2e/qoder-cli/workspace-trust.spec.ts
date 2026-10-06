import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { exerciseNativeWorkspaceTrustLimit, mcpServerProjectConfiguration } from '../helpers/nativeWorkspaceTrustLimit'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: mcpServerProjectConfiguration((directory, server) => {
      const configuration = join(directory, '.qoder', 'settings.json')
      mkdirSync(dirname(configuration), { recursive: true })
      writeFileSync(configuration, JSON.stringify({ mcpServers: { [server.name]: { command: server.command, args: server.args } } }))
    }),
  })
})
