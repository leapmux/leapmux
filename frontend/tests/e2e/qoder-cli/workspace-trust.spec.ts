import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeWorkspaceTrustLimit, expectMcpServerLoaded } from '../helpers/nativeWorkspaceTrustLimit'
import { createGitRepo } from '../helpers/worktree'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory }) => {
        createGitRepo(directory, '.')
        const script = writeMcpEchoServer(directory, { receiptLog: join(directory, 'workspace-mcp-receipt.json') })
        const configuration = join(directory, '.qoder', 'settings.json')
        mkdirSync(dirname(configuration), { recursive: true })
        writeFileSync(configuration, JSON.stringify({ mcpServers: { trust_probe: { command: process.execPath, args: [script] } } }))
      },
      prove: async (privateContext, { directory }) => {
        await sendNativeAnswer(privateContext, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
        await expectMcpServerLoaded(join(directory, 'workspace-mcp-receipt.json'))
      },
    },
  })
})
