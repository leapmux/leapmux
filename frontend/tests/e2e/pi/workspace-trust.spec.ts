import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openAgentViaAPI } from '../helpers/api'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText, nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeWorkspaceTrustLimit } from '../helpers/nativeWorkspaceTrustLimit'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { tabById } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { writePiMcpConfiguration } from './mcpConfiguration'
import { nativeContext } from './scenarios'
import { withMockPiModel } from './scriptedModel'

/**
 * The key of the project MCP server. The run environment already registers the echo server for Pi under its own name
 * (`createPiEnvironment`), so the project file uses another key. A tool under this key can then only come from the
 * project file of this test.
 */
const PROJECT_MCP_SERVER = 'trust_probe'

/** The name of the echo tool of {@link PROJECT_MCP_SERVER} in the catalog that Pi offers the model. */
const PROJECT_MCP_TOOL = mcpToolCall(AgentProvider.PI, 'catalog', { server: PROJECT_MCP_SERVER, tool: 'echo', input: {} }).name

piTest('starts and reads a private project without a workspace trust request', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await exerciseNativeWorkspaceTrustLimit(context, {
    projectConfiguration: {
      prepare({ directory, marker }) {
        writePiMcpConfiguration(directory, getGlobalState().tmpDir, { [PROJECT_MCP_SERVER]: writeMcpEchoServer(directory) })
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
        expect(nativeModelToolNames(untrusted)).not.toContain(PROJECT_MCP_TOOL)
        expect(nativeModelInstructionText(untrusted)).not.toContain(marker)
        expect(existsSync(receipt)).toBe(false)
        await withMockPiModel(directory, leapmuxServer, async (settings) => {
          const server = privateContext.leapmuxServer
          const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, privateContext.workspaceId, directory, { agentProvider: AgentProvider.PI, ...settings })
          await tabById(page, agentId).click()
          const trusted = await sendNativeAnswer(privateContext, 'Reply once after the isolated native trust decision.', 'The trusted native project configuration turn completed.')
          expect(nativeModelToolNames(trusted)).toContain('native_project_trust_probe')
          expect(nativeModelToolNames(trusted)).toContain(PROJECT_MCP_TOOL)
          expect(nativeModelInstructionText(trusted)).toContain(marker)
          expect(readFileSync(receipt, 'utf8')).toBe(marker)
        })
      },
    },
  })
})
