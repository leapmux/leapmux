import { Buffer } from 'node:buffer'
import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { withNativeStartupWorker } from '../helpers/nativeStartupWorker'
import { resolveNativeStartupLaunch } from '../helpers/nativeStartupWrapper'
import { codebuddyReplToolCall } from '../helpers/providerToolCalls'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'
import { codebuddyReplSchema } from './toolCatalog'

codebuddyTest('runs native code and retains computed output and script errors after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const initial = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const launch = resolveNativeStartupLaunch(leapmuxServer.agentEnv, { binaryName: 'codebuddy', args: ['--agent', 'ptc'] })
  // The native PTC mode selects REPL as its sole direct tool.
  await withNativeStartupWorker(initial, launch, {}, async (workerId, wrapper) => {
    const server = { ...leapmuxServer, workerId }
    const context = await nativeContext({ page, modelScript, leapmuxServer: server, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const opening = openProviderAgent(server, context.workspaceId, context.providerAgent, { directoryPrefix: 'native-code-execution-' })
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
