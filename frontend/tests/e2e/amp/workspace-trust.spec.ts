import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect } from '@playwright/test'
import { ampExtractControl } from '../../../src/components/chat/providers/amp/extractControl'
import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { ampTest } from '../amp-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-ask', classify: ampExtractControl })
})

ampTest('keeps untrusted project MCP servers blocked without an interactive trust decision', async ({ native }, testInfo) => {
  let expectedConfiguration: { command: string, args: string[] } | undefined
  await exerciseNativeWorkspaceTrustLimit(native, {
    optionValues: { permissionMode: AMP_PERMISSION_MODE.AllowAll },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const server = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        expectedConfiguration = { command: server.command, args: [...server.args] }
        const config = join(directory, '.amp', 'settings.json')
        mkdirSync(dirname(config), { recursive: true })
        writeFileSync(config, JSON.stringify({ 'amp.mcpServers': { [marker]: expectedConfiguration } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        const receiptLog = join(directory, 'native-project-mcp-receipt.json')
        await sendNativeAnswer(privateContext, 'Reply once while the untrusted project MCP server awaits approval.', 'The first native project trust turn completed.')
        expect(existsSync(receiptLog)).toBe(false)
        const { tools, workspaceMcpConfiguration } = await readAmpExecutorCatalog(privateContext, { workspaceMcpServer: marker, onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
        if (!expectedConfiguration)
          throw new Error('The native project MCP configuration did not complete preparation.')
        expect(workspaceMcpConfiguration).toEqual(expectedConfiguration)
        expect(tools).not.toContain(`mcp__${marker}__echo`)
        expect(existsSync(receiptLog)).toBe(false)
        await sendNativeAnswer(privateContext, 'Complete another turn without granting native project MCP trust.', 'The second native project trust turn completed.')
        expect(existsSync(receiptLog)).toBe(false)
      },
    },
  })
})
