import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { kimiExtractControl } from '../../../src/components/chat/providers/kimi/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { kimiTest } from '../kimi-fixtures'

// LeapMux exposes no interactive native workspace-trust route for this provider.
kimiTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await chooseSettingsOption(page, 'permissionMode-manual')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-workspace-trust-control.txt', callId: 'native-workspace-trust-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: kimiExtractControl,
    nativeOperation: beforeDecision => exerciseNativePermissionDecision(context, {
      toolCall: operation.toolCall,
      decision: 'allow',
      beforeDecision: async (banner) => {
        await operation.beforeDecision()
        await beforeDecision(banner)
      },
      nativeProof: operation.nativeProof,
    }),
  })
})

kimiTest('keeps untrusted project MCP configuration unloaded without a browser trust route', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await exerciseNativeWorkspaceTrustLimit(context, {
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
