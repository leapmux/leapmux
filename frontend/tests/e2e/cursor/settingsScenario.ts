import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentOptionSettlementState, UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { getTestChannel } from '../helpers/api'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseNativeOption, exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { waitForAgentStartupViaAPI } from '../helpers/workerTabs'

/** Read selected settings only from the actual decoded native Run frame. */
export function expectCursorNativeSettings(request: MockModelRequestRecord, options: { model?: boolean, plan?: boolean, effort?: 'low' | 'xhigh' }): void {
  expect(request.nativeRequest).toMatchObject({
    ...(options.model
      ? { requestedModel: {
          modelId: 'mock-grok',
          maxMode: false,
          parameters: [{ id: 'context', value: '256k' }, { id: 'reasoning_effort', value: options.effort ?? 'low' }],
        } }
      : {}),
    ...(options.plan ? { mode: 3 } : {}),
  })
}

/** Select two effort levels of the same model, then retain the final level after reload. */
export async function exerciseCursorReasoningEffort(context: ManagedNativeScenarioContext): Promise<void> {
  const low = 'mock-grok[context=256k,reasoning_effort=low]'
  await chooseSettingsOption(context.page, `model-${low}`)
  await waitForSettingsIdle(context.page)
  const first = await sendNativeAnswer(context, 'Reply once at the selected low effort.', 'The native low effort turn ended.')
  expectCursorNativeSettings(first, { model: true, effort: 'low' })
  await exerciseNativeOption(context, {
    groupId: 'model',
    value: 'mock-grok[context=256k,reasoning_effort=xhigh]',
    nativeProof(request) {
      expectCursorNativeSettings(request, { model: true, effort: 'xhigh' })
    },
  })
}

/** Prove the native model variant and Plan enum before and after restoring the selected axis. */
export async function exerciseCursorSelectedPlanSettings(context: ManagedNativeScenarioContext, restoreGroup: 'model' | 'permissionMode'): Promise<void> {
  const value = 'mock-grok[context=256k,reasoning_effort=low]'
  await chooseSettingsOption(context.page, `model-${value}`)
  await chooseSettingsOption(context.page, 'permissionMode-plan')
  await waitForSettingsIdle(context.page)
  await expectSettingsOptionChosen(context.page, `model-${value}`)
  await expectSettingsChip(context.page, 'Plan')
  const request = await sendNativeAnswer(context, 'Reply once after the settings change.', 'Settings applied.')
  expectCursorNativeSettings(request, { model: true, plan: true })
  await exerciseRestoredNativeOption(context, {
    groupId: restoreGroup,
    value: restoreGroup === 'model' ? value : 'plan',
    nativeProof(restored) {
      expectCursorNativeSettings(restored, { model: true, plan: true })
    },
  })
  await expectSettingsOptionChosen(context.page, `model-${value}`)
  await expectSettingsChip(context.page, 'Plan')
}

/** Keep the actual confirmed set-mode RPC and verify its next native request after reload. */
export async function exerciseCursorNativePlanRPC(context: ManagedNativeScenarioContext): Promise<void> {
  const server = context.leapmuxServer
  const agents = await waitForAgentStartupViaAPI(server.hubUrl, server.adminToken, server.workerId, context.workspaceId)
  expect(agents).toHaveLength(1)
  await waitForSettingsHydrated(context.page)
  const agent = agents[0]
  if (!agent)
    throw new Error('Cursor has no running agent for the mode request.')
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const response = await channel.callWorker(server.workerId, 'UpdateAgentSettings', UpdateAgentSettingsRequestSchema, UpdateAgentSettingsResponseSchema, {
    agentId: agent.id,
    settings: { options: { permissionMode: 'plan' } },
  })
  expect(response.optionSettlements.permissionMode?.state).toBe(AgentOptionSettlementState.CONFIRMED)
  expect(response.optionSettlements.permissionMode?.value).toBe('plan')
  const request = await sendNativeAnswer(context, 'Reply once after the native mode RPC.', 'Settings applied.')
  expectCursorNativeSettings(request, { plan: true })
  await exerciseRestoredNativeOption(context, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof(restored) {
      expectCursorNativeSettings(restored, { plan: true })
    },
  })
  await expectSettingsChip(context.page, 'Plan')
}
