import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText, nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { getGlobalState } from '../helpers/server'
import { tabById } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { nativeContext } from './scenarios'
import { withMockPiModel } from './scriptedModel'

piTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare({ directory, marker }) {
        const mcp = writeMcpEchoServer(directory).script
        writePiMcpConfiguration(directory, getGlobalState().tmpDir, { trust_probe: { command: process.execPath, args: [mcp] } })
        const extensions = join(directory, '.pi', 'extensions')
        mkdirSync(extensions, { recursive: true })
        const receipt = join(directory, 'native-project-extension-loaded.txt')
        writeFileSync(join(extensions, 'trust-probe.ts'), `
import { writeFileSync } from 'node:fs';
export default function (pi) {
  writeFileSync(${JSON.stringify(receipt)}, ${JSON.stringify(marker)});
  pi.on('before_agent_start', (event) => ({ systemPrompt: event.systemPrompt + '\\n' + ${JSON.stringify(marker)} }));
  pi.registerTool({ name: 'native_project_trust_probe', label: 'Native project trust probe', description: 'Read the native project configuration state.', parameters: { type: 'object', properties: {}, additionalProperties: false }, async execute() { return { content: [{ type: 'text', text: ${JSON.stringify(marker)} }], details: {} }; } });
}
`)
      },
      async prove(privateContext, { directory, marker }) {
        const receipt = join(directory, 'native-project-extension-loaded.txt')
        const untrusted = await sendNativeAnswer(privateContext, 'Reply once after native project trust resolves.', 'The native project configuration turn completed.')
        expect(nativeModelToolNames(untrusted)).not.toContain('native_project_trust_probe')
        expect(nativeModelToolNames(untrusted)).not.toContain('mcp__trust_probe__echo')
        expect(nativeModelInstructionText(untrusted)).not.toContain(marker)
        expect(existsSync(receipt)).toBe(false)
        await withMockPiModel(directory, leapmuxServer, async (settings) => {
          const server = privateContext.leapmuxServer
          const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, privateContext.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
          await tabById(page, agentId).click()
          const trusted = await sendNativeAnswer(privateContext, 'Reply once after the isolated native trust decision.', 'The trusted native project configuration turn completed.')
          expect(nativeModelToolNames(trusted)).toContain('native_project_trust_probe')
          expect(nativeModelToolNames(trusted)).toContain('mcp__trust_probe__echo')
          expect(nativeModelInstructionText(trusted)).toContain(marker)
          expect(readFileSync(receipt, 'utf8')).toBe(marker)
        })
      },
    },
  })
})
