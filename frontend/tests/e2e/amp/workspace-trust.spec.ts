import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { ampExtractControl } from '../../../src/components/chat/providers/amp/extractControl'
import { AMP_PERMISSION_MODE } from '../../../src/generated/contracts/amp-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision } from '../helpers/nativePermission'

import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'

import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedNativeControl } from '../helpers/unsupportedNativeControl'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

// LeapMux exposes no interactive native workspace-trust route for this provider.
ampTest('classifies real native controls and proves the missing workspace-trust route', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  await chooseSettingsOption(page, 'permissionMode-ask')
  await waitForSettingsIdle(page)
  const operation = await createNativePermissionFileWrite(context, { fileName: 'native-workspace-trust-control.txt', callId: 'native-workspace-trust-permission', outputPrefix: 'NATIVECONTROL' })
  await exerciseUnsupportedNativeControl(context, {
    purpose: 'workspace-trust',
    classify: ampExtractControl,
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

ampTest('keeps untrusted project MCP servers blocked without an interactive trust decision', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, testInfo) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  let expectedConfiguration: { command: string, args: string[] } | undefined
  await exerciseNativeWorkspaceTrustLimit(context, {
    optionValues: { permissionMode: AMP_PERMISSION_MODE.AllowAll },
    projectConfiguration: {
      prepare: ({ directory, marker }) => {
        const script = writeMcpEchoServer(directory, { receiptLog: join(directory, 'native-project-mcp-receipt.json') })
        expectedConfiguration = { command: process.execPath, args: [script] }
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
