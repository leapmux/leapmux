import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { kimiExtractControl } from '../../../src/components/chat/providers/kimi/extractControl'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { kimiTest } from '../kimi-fixtures'

kimiTest('classifies real native controls and proves the missing workspace-trust route', async ({ native }) => {
  await exerciseMissingWorkspaceTrustRoute(native, { askOption: 'permissionMode-manual', classify: kimiExtractControl })
})

kimiTest('keeps untrusted project MCP configuration unloaded without a browser trust route', async ({ native }) => {
  await exerciseNativeWorkspaceTrustLimit(native, {
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const script = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        const config = join(directory, '.mcp.json')
        mkdirSync(dirname(config), { recursive: true })
        writeFileSync(config, JSON.stringify({ mcpServers: { [marker]: { transport: 'stdio', command: process.execPath, args: [script] } } }))
      },
      prove: async (privateContext, { directory, marker }) => {
        const request = await sendNativeAnswer(privateContext, 'Reply once in the new untrusted project.', 'The native project turn completed.')
        expect(nativeModelToolNames(request).some(name => name.includes(marker))).toBe(false)
        expect(existsSync(join(directory, 'native-project-mcp-receipt.json'))).toBe(false)
        const next = await sendNativeAnswer(privateContext, 'Continue the same native project once.', 'The second project turn completed.')
        expect(nativeModelToolNames(next).some(name => name.includes(marker))).toBe(false)
        expect(existsSync(join(directory, 'native-project-mcp-receipt.json'))).toBe(false)
      },
    },
  })
})
