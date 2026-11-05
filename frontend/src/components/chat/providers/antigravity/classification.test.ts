import { describe, expect, it } from 'vitest'
import { ANTIGRAVITY_FRAME, ANTIGRAVITY_STEP_TYPE } from '~/generated/contracts/antigravity-protocol'
import { classifyAntigravityMessage } from './classification'
import { antigravityExtractRow } from './extractors/row'
import { antigravitySpanRole } from './spanRole'

function input(parentObject: Record<string, unknown>) {
  return { parentObject, wrapper: undefined } as never
}

describe('classifyAntigravityMessage', () => {
  it('classifies steps, interactions and turn ends', () => {
    expect(classifyAntigravityMessage(input({ name: ANTIGRAVITY_FRAME.TurnEnd })).kind).toBe('result_divider')
    expect(classifyAntigravityMessage(input({ name: ANTIGRAVITY_FRAME.Interaction, modelRequest: {} })).kind).toBe('notification')
    expect(classifyAntigravityMessage(input({
      type: ANTIGRAVITY_STEP_TYPE.PlannerResponse,
      plannerResponse: { modifiedResponse: 'text' },
    })).kind).toBe('assistant_text')
    expect(classifyAntigravityMessage(input({ type: ANTIGRAVITY_STEP_TYPE.RunCommand, status: 'CORTEX_STEP_STATUS_RUNNING' })).kind).toBe('tool_use')
    expect(classifyAntigravityMessage(input({ type: ANTIGRAVITY_STEP_TYPE.RunCommand, status: 'CORTEX_STEP_STATUS_DONE' })).kind).toBe('tool_result')
    expect(classifyAntigravityMessage(input({ type: ANTIGRAVITY_STEP_TYPE.UserInput })).kind).toBe('user_content')
    expect(classifyAntigravityMessage(input({ type: ANTIGRAVITY_STEP_TYPE.SystemMessage })).kind).toBe('hidden')
    const status = { type: 'agent_status', text: 'Extension X announced Y' }
    expect(classifyAntigravityMessage({
      parentObject: status,
      wrapper: {
        messages: [status],
      },
    } as never).kind).toBe('notification')
    expect(classifyAntigravityMessage(input({ type: 'CORTEX_STEP_TYPE_FUTURE', status: 'CORTEX_STEP_STATUS_DONE' })).kind).toBe('tool_result')
  })
})

describe('antigravityExtractRow', () => {
  it('reads the original child prompt from native user input', () => {
    expect(antigravityExtractRow({
      category: { kind: 'user_content' },
      resolved: {
        parentObject: {
          userInput: {
            items: [
              { text: 'first part' },
              { text: 'second part' },
            ],
          },
        },
      },
    } as never)).toEqual({
      kind: 'user',
      text: 'first part\nsecond part',
      attachments: [],
    })
  })
})

describe('antigravitySpanRole', () => {
  it('opens a span for a tool and none for a planner step', () => {
    expect(antigravitySpanRole({
      parentObject: { type: ANTIGRAVITY_STEP_TYPE.RunCommand, status: 'CORTEX_STEP_STATUS_RUNNING' },
    } as never)).toBe('request')
    expect(antigravitySpanRole({
      parentObject: { type: ANTIGRAVITY_STEP_TYPE.PlannerResponse, status: 'CORTEX_STEP_STATUS_DONE' },
    } as never)).toBe('result')
  })
})
