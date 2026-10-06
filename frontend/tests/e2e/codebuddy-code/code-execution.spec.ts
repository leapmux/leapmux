import { Buffer } from 'node:buffer'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { codebuddyTest } from '../codebuddy-fixtures'
import { expect } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { codebuddyReplToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { openWorkspace } from '../helpers/ui'
import { codebuddyReplSchema } from './toolCatalog'

codebuddyTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const initial = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEBUDDY }
  const launch = resolveNativeStartupLaunch(leapmuxServer.agentEnv, { binaryName: 'codebuddy', args: ['--agent', 'ptc'] })
  // The native PTC mode selects REPL as its sole direct tool.
  await withNativeStartupWorker(initial, launch, {}, async (workerId, wrapper) => {
    const server = { ...leapmuxServer, workerId }
    const context = { page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CODEBUDDY }
    const opening = openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId, createTestDirectory('native-code-execution-'), agentOpenOptions(context.provider))
    const entered = await Promise.race([wrapper.entry, opening.then(() => wrapper.entry)])
    expect(entered.argv).toContain('--input-format')
    await wrapper.release()
    await opening
    await openWorkspace(page, context.workspaceId)
    await exerciseNativeCodeExecution(context, {
      toolCall: codebuddyReplToolCall,
      catalogProof: (request) => {
        expect(nativeModelToolNames(request)).toEqual(['REPL'])
        codebuddyReplSchema(nativeCodeExecutionSchema(request, 'REPL', { code: 'string' }))
      },
      nativeProof: async (_request, callId) => {
        const agent = await currentNativeAgent(context)
        const snapshot = await readNativeMessageSnapshot(context, agent.id)
        const rows = snapshot.messages
          .map(message => ({ messageId: message.id, seq: message.seq.toString(), spanId: message.spanId, spanType: message.spanType, source: message.source, completion: message.completion, body: nativeMessageBody(message) }))
          .filter(row => JSON.stringify(row.body).includes(`"${callId}"`))
        await testInfo.attach(`codebuddy-native-repl-${callId}`, { body: Buffer.from(JSON.stringify({ agentId: snapshot.agentId, nativeSessionId: snapshot.agentSessionId, rows }, null, 2)), contentType: 'application/json' })
      },
      scripts: marker => [
        { label: 'output', source: `return ${JSON.stringify(marker)} + (40 + 2);`, expected: `${marker}42`, failed: false },
        { label: 'failure', source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`, expected: `${marker}77`, failed: true },
      ],
    })
  })
})
