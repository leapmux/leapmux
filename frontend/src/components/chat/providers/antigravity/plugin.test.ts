import { describe, expect, it, vi } from 'vitest'
import { ANTIGRAVITY_CONTROL_PAYLOAD, ANTIGRAVITY_CONTROL_TOOL, ANTIGRAVITY_MODE } from '~/generated/contracts/antigravity-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { buildDenyResponse } from '~/utils/controlResponse'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'
import './plugin'

const plugin = providerFor(AgentProvider.ANTIGRAVITY_CLI)!

describe('antigravity plugin metadata', () => {
  it('registers every capability', () => {
    expect(plugin.transcript.classify).toBeDefined()
    expect(plugin.transcript.extractRow).toBeDefined()
    expect(plugin.transcript.extractDivider).toBeDefined()
    expect(plugin.controls?.extractControl).toBeDefined()
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: true,
      audio: true,
      binary: true,
    })
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })

  it('maps only the bypass preset', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: ANTIGRAVITY_MODE.Bypass },
      },
    })
    expect(plugin.controls?.permissionPresets?.smart).toBeUndefined()
  })
})

describe('antigravity plugin controls', () => {
  it('distinguishes permissions and plans from questions', () => {
    const isRequest = plugin.controls!.askUserQuestion!.isRequest
    expect(isRequest({
      requestedInteraction: {
        permission: {
          resource: { action: 'command' },
        },
      },
    })).toBe(false)
    expect(isRequest({
      request: { tool_name: ANTIGRAVITY_CONTROL_TOOL.PlanReview },
    })).toBe(false)
    expect(isRequest({
      requestedInteraction: {
        askQuestion: { questions: [] },
      },
    })).toBe(true)
  })

  it('builds the native permission answer with denial feedback', () => {
    const response = plugin.controls!.buildControlResponse!({}, 'Use another approach', 'antigravity:cascade:trajectory:4')
    expect(response).toEqual({
      cascadeId: 'cascade',
      interaction: {
        trajectoryId: 'trajectory',
        stepIndex: 4,
        permission: { allow: false, scope: 'PERMISSION_SCOPE_ONCE', userDenyInstruction: 'Use another approach' },
      },
    })
  })

  it('routes a plan review to the plan card', () => {
    expect(plugin.controls!.extractControl!({
      payload: {
        request: {
          tool_name: ANTIGRAVITY_CONTROL_TOOL.PlanReview,
          input: { [ANTIGRAVITY_CONTROL_PAYLOAD.PlanContent]: '# Complete native plan\n\n1. Inspect the source.' },
        },
      },
    })).toEqual({ kind: 'plan', text: '# Complete native plan\n\n1. Inspect the source.' })
  })

  it('builds a neutral denial for a plan review', () => {
    const payload = {
      request: { tool_name: ANTIGRAVITY_CONTROL_TOOL.PlanReview },
    }
    expect(plugin.controls!.buildControlResponse!(payload, 'Revise the plan', 'antigravity-plan:review-1')).toEqual(buildDenyResponse('antigravity-plan:review-1', 'Revise the plan'))
  })

  it('answers single and multi-select questions natively', async () => {
    const request = {
      requestId: 'antigravity:child:trajectory-child:8',
      payload: {
        requestedInteraction: {
          askQuestion: {
            questions: [
              {
                question: 'First?',
                options: [
                  { id: 'a', text: 'A' },
                  { id: 'b', text: 'B' },
                ],
                isMultiSelect: false,
              },
              {
                question: 'Second?',
                options: [
                  { id: 'x', text: 'X' },
                  { id: 'y', text: 'Y' },
                ],
                isMultiSelect: true,
              },
            ],
          },
        },
      },
    }
    const state = createControlAnswerState({
      selections: {
        0: ['a'],
        1: ['x', 'y'],
      },
    })
    const send = vi.fn().mockResolvedValue(undefined)
    await plugin.controls!.askUserQuestion!.sendAnswer(request as never, send, plugin.controls!.askUserQuestion!.extractQuestions(request.payload), state)
    expect(JSON.parse(new TextDecoder().decode(send.mock.calls[0]![0] as Uint8Array))).toEqual({
      cascadeId: 'child',
      interaction: {
        trajectoryId: 'trajectory-child',
        stepIndex: 8,
        askQuestion: {
          responses: [
            {
              question: 'First?',
              options: [
                { id: 'a', text: 'A' },
                { id: 'b', text: 'B' },
              ],
              isMultiSelect: false,
              selectedOptionIds: ['a'],
            },
            {
              question: 'Second?',
              options: [
                { id: 'x', text: 'X' },
                { id: 'y', text: 'Y' },
              ],
              isMultiSelect: true,
              selectedOptionIds: ['x', 'y'],
            },
          ],
        },
      },
    })
  })
})
