import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpResultServer } from '../helpers/mcpResultServer'
import { readMcpCallArguments } from '../helpers/mcpServerReceipt'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withNativeWorker } from '../helpers/nativeWorker'
import { createTestDirectory } from '../helpers/runDirectory'
import { opencodeMcpServerConfiguration } from './mcpLimit'
import { nativeContext } from './scenarios'

/**
 * Run the native v2 code executor on a private Worker.
 * The Worker adds one result server to the isolated inline configuration.
 *
 * Four scripts prove these outcomes:
 *
 * - Computed output.
 * - A script error.
 * - A nested MCP call.
 * - A nested MCP call that fails.
 *
 * {@link exerciseNativeCodeExecution} checks each outcome in the model request, in the transcript, and in the
 * transcript after a reload. The receipt of the result server must then hold exactly the two nested calls.
 */
export async function exerciseOpenCodeCodeExecution(context: ManagedNativeScenarioContext): Promise<void> {
  const original = context.leapmuxServer.agentEnv?.OPENCODE_CONFIG_CONTENT
  if (!original)
    throw new Error('The native executor requires its isolated provider configuration.')
  const directory = createTestDirectory('native-code-mcp-')
  const receiptLog = join(directory, 'native-code-mcp-receipt.json')
  const server = writeMcpResultServer(directory, { receiptLog })
  const content = opencodeMcpServerConfiguration(original, server)
  await withNativeWorker(context.leapmuxServer, {
    dataDirPrefix: 'native-code-worker',
    workerName: 'Native code executor',
    env: { OPENCODE_CONFIG_CONTENT: content },
  }, async ({ server: worker }) => {
    const privateContext = await nativeContext({ ...context, leapmuxServer: worker })
    await openNativeAgent(privateContext, { directoryPrefix: 'native-code-execution-' })
    await exerciseNativeCodeExecution(privateContext, {
      catalogProof: (request) => {
        nativeCodeExecutionSchema(request, 'execute', { code: 'string' })
      },
      nativeProof: async (_request, callId) => {
        if (callId !== 'native-code-2' && callId !== 'native-code-3')
          return
        const calls = readMcpCallArguments(receiptLog)
        if (callId === 'native-code-2') {
          expect(calls).toHaveLength(1)
          expect(calls[0]).toEqual({
            name: 'inspect',
            arguments: { count: 0, enabled: false, text: 'native-nested-value' },
          })
        }
        else {
          expect(calls).toHaveLength(2)
          expect(calls[1]).toEqual({ name: 'fail', arguments: {} })
        }
      },
      scripts: marker => [
        {
          label: 'output',
          source: `return ${JSON.stringify(marker)} + (40 + 2);`,
          expected: `${marker}42`,
          failed: false,
        },
        {
          label: 'failure',
          source: `throw new Error(${JSON.stringify(marker)} + (70 + 7));`,
          expected: `${marker}77`,
          failed: true,
        },
        {
          label: 'nested MCP',
          source: `const value = await tools.${server.name}.inspect({ count: 0, enabled: false, text: "native-nested-value" }); return ${JSON.stringify(marker)} + value.nextCount;`,
          expected: `${marker}1`,
          failed: false,
        },
        {
          label: 'nested MCP failure',
          source: `return await tools.${server.name}.fail({});`,
          expected: 'NATIVE_MCP_FAILED_RESULT',
          failed: true,
        },
      ],
    })
    // The checks above select their script by its call ID. This check holds whatever the IDs are.
    expect(readMcpCallArguments(receiptLog)).toEqual([
      {
        name: 'inspect',
        arguments: { count: 0, enabled: false, text: 'native-nested-value' },
      },
      { name: 'fail', arguments: {} },
    ])
  })
}
