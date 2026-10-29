import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { openAgentViaAPI } from '../helpers/api'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { withPrivateNativeWorkspace } from '../helpers/privateNativeWorkspace'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'
import { OH_MY_PI_FAST_MODEL_ID, prepareFastModeOhMyPi } from './fixtures'
import { nativeContext, OH_MY_PI_AGENT } from './scenarios'

ohMyPiTest('changes native service priority independently and preserves it across a provider restart', async ({ page, leapmuxServer, modelScript }) => {
  await withPrivateNativeWorkspace(page, leapmuxServer, {
    prefix: 'omp-fast',
    workerName: 'Oh My Pi fast-mode test',
    providerAgent: OH_MY_PI_AGENT,
    prepare: runDirectory => prepareFastModeOhMyPi(runDirectory, leapmuxServer.mockModelUrl),
    openAgent: (server, workspaceId, workingDir) => openAgentViaAPI(server, workspaceId, workingDir, {
      agentProvider: AgentProvider.OH_MY_PI,
      model: OH_MY_PI_FAST_MODEL_ID,
      optionValues: { permissionMode: 'yolo', effort: 'high', fastMode: 'false' },
    }),
  }, async ({ server, workspaceId }) => {
    const native = await nativeContext({
      page,
      leapmuxServer: server,
      workspaceId,
      modelScript,
    })
    let initialEffort: unknown
    await exerciseNativeOptionSequence(native, {
      groupId: 'fastMode',
      steps: [
        { value: 'false', via: 'default' },
        { value: 'true', via: 'choose' },
        { value: 'true', via: 'reload' },
        { value: 'true', via: 'default' },
        { value: 'false', via: 'choose' },
      ],
      nativeProof: async (request, step, index) => {
        expect(request.protocol).toBe('openai-chat-completions')
        if (!isObject(request.body))
          throw new Error('The native fast-mode request is not an object.')
        expect(request.body.model).toBe('gpt-5.5')
        if (index === 0)
          initialEffort = request.body.reasoning_effort
        expect(request.body.reasoning_effort).toBe(initialEffort)
        if (step.value === 'true')
          expect(request.body.service_tier).toBe('priority')
        else
          expect(request.body).not.toHaveProperty('service_tier')
        if (index === 2) {
          // Output style requires a native process restart; the next request must retain priority.
          await chooseSettingsOption(page, 'outputStyle-friendly')
          await waitForSettingsIdle(page)
        }
      },
    })
  })
})
