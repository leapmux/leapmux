import { appendFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { createMockAgentEnvironment } from '../helpers/mockAgentEnvironment'
import { exerciseNativeCodeExecution, nativeCodeExecutionSchema } from '../helpers/nativeCodeExecution'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests, nativeContext, REASONIX_AGENT } from './scenarios'

reasonixTest('runs configured native Starlark, reports script errors, and calls agent tools after reload', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'rx-code',
    workerName: 'Reasonix code execution test',
    providerAgent: REASONIX_AGENT,
    prepare: async (runDirectory) => {
      const environment = await createMockAgentEnvironment(runDirectory, leapmuxServer.mockModelUrl)
      appendFileSync(join(environment.homeDir, '.reasonix', 'config.toml'), '\n[agent]\ncode_mode = true\n')
      return { agentEnv: environment.env, setup: undefined }
    },
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, agentOpenOptions(REASONIX_AGENT.provider)),
  }, async ({ server, workspaceId, workingDir }) => {
    const native = await nativeContext({
      page,
      leapmuxServer: server,
      workspaceId,
      modelScript,
    })
    await exerciseNativeCodeExecution(native, {
      prepare: () => bypassToolRequests(native),
      catalogProof: (request) => {
        const schema = nativeCodeExecutionSchema(request, 'run_script', { script: 'string' })
        expect(schema.required).toEqual(['script'])
        expect(schema.additionalProperties).toBe(false)
      },
      scripts: marker => [
        {
          label: 'output',
          source: `print(${JSON.stringify(marker)} + str(40 + 2))`,
          expected: `${marker}42`,
          failed: false,
        },
        {
          label: 'failure',
          source: `fail(${JSON.stringify(marker)} + str(70 + 7))`,
          expected: `${marker}77`,
          failed: true,
        },
        {
          label: 'tool calls',
          source: `call("write_file", path="reasonix-script.txt", content=str(6 * 7))\nprint(${JSON.stringify(marker)} + call("read_file", path="reasonix-script.txt"))`,
          expected: `${marker}42`,
          failed: false,
        },
      ],
      nativeProof: async (_request, callId) => {
        if (callId === 'native-code-2')
          expect(readFileSync(join(workingDir, 'reasonix-script.txt'), 'utf8')).toBe('42')
      },
    })
  })
})
