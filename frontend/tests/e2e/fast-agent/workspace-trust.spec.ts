import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { ensureGitRepositoryRoot } from '../helpers/worktree'
import { nativeContext } from './scenarios'

fastAgentTest('starts with a real project configuration and no native workspace trust barrier', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare: ({ directory }) => {
        ensureGitRepositoryRoot(directory)
        const { script } = writeMcpEchoServer(directory, { receiptLog: join(directory, 'workspace-mcp-receipt.json') })
        writeFileSync(join(directory, 'fast-agent.yaml'), `mcp:\n  servers:\n    trust_probe:\n      command: ${JSON.stringify(process.execPath)}\n      args: [${JSON.stringify(script)}]\n`)
      },
      prove: async (privateContext, { directory }) => {
        const request = await sendNativeAnswer(privateContext, 'Return one native response from this scratch project.', 'The native project configuration probe completed.')
        expect(nativeModelToolNames(request).some(name => name.includes('trust_probe'))).toBe(false)
        expect(existsSync(join(directory, 'workspace-mcp-receipt.json'))).toBe(false)
      },
    },
  })
})
