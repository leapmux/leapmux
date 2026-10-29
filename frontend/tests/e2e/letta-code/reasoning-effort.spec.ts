import { expect } from '@playwright/test'
import { LETTA_REASONING_MODEL_ID, LETTA_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOptionSequence, exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { LETTA_E2E_SKIP_REASON, lettaTest } from '../letta-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { LETTA_TITLE_RULE } from './housekeeping'
import { LETTA_AGENT, nativeContext } from './scenarios'

lettaTest('applies effort and restores the native default without replacing the session', async ({ authenticatedReasoningLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedReasoningLettaWorkspace.workspaceId,
  })
  const before = await currentNativeAgent(context)
  await exerciseNativeOptionSequence(context, {
    groupId: 'effort',
    steps: [
      { value: 'low', via: 'choose' },
      { value: 'high', via: 'choose' },
      { value: 'high', via: 'reload' },
      { value: 'auto', via: 'choose' },
      { value: 'auto', via: 'reload' },
    ],
    nativeProof: (request, step) => {
      expect(request.body).toHaveProperty('model', 'gpt-5.4')
      expect(request.body).toHaveProperty('reasoning.effort', step.value === 'auto'
        ? 'none'
        : step.value)
    },
  })
  expect((await currentNativeAgent(context)).agentSessionId).toBe(before.agentSessionId)
})

lettaTest('hides effort on a plain model and restores the reasoning catalog on return', async ({ authenticatedReasoningLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedReasoningLettaWorkspace.workspaceId,
  })
  await exerciseEffortModelRoundTrip(context, {
    effortGroupId: 'effort',
    model: LETTA_REASONING_MODEL_ID,
    chosen: 'high',
    via: LETTA_VISION_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'none',
    nativeProof: (request) => {
      expect(request.body).toHaveProperty('model', 'gpt-5.4')
      expect(request.body).toHaveProperty('reasoning.effort', 'none')
    },
  })
})

lettaTest('keeps an offered effort when switching reasoning models', async ({ authenticatedReasoningLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: authenticatedReasoningLettaWorkspace.workspaceId,
  })
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'high' },
    model: 'openai/gpt-5.5',
    nativeProof: (request) => {
      expect(request.body).toHaveProperty('model', 'gpt-5.5')
      expect(request.body).toHaveProperty('reasoning.effort', 'high')
    },
  })
})

const restartTest = createProviderProcessTest({
  agent: LETTA_AGENT,
  nativeContext,
  skipReason: LETTA_E2E_SKIP_REASON,
  modelRules: [LETTA_TITLE_RULE],
  openOptions: {
    model: LETTA_REASONING_MODEL_ID,
    optionValues: { effort: 'high' },
  },
})

restartTest('applies a configured startup effort and retains it across a worker restart', async ({ native, separateHubWorker }) => {
  await exerciseNativeOptionSequence(native, {
    groupId: 'effort',
    steps: [
      { value: 'high', via: 'default' },
    ],
    nativeProof: request => expect(request.body).toHaveProperty('reasoning.effort', 'high'),
  })
  await exerciseWorkerRestart(native, separateHubWorker, 'worker')
  await exerciseRestoredNativeOption(native, {
    groupId: 'effort',
    value: 'high',
    nativeProof: request => expect(request.body).toHaveProperty('reasoning.effort', 'high'),
  })
})
