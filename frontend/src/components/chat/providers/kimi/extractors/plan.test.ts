import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { kimiTranscriptToolFrame } from '~/test-support/kimiFixtures'
import { kimiPlanText } from './plan'

describe('kimiPlanText', () => {
  it('reads the plan an ExitPlanMode call proposes', () => {
    expect(kimiPlanText(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan' },
    }))).toBe('# Plan')
  })

  it('reads no plan elsewhere', () => {
    expect(kimiPlanText(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      display: { kind: 'plan_review', plan: '  ' },
    }))).toBeNull()
    expect(kimiPlanText(kimiTranscriptToolFrame({ toolCallId: 'c', name: KIMI_TOOL.ExitPlanMode, input: {} }))).toBeNull()
    expect(kimiPlanText(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.Bash,
      input: {},
      display: { kind: 'plan_review', plan: '# Plan' },
    }))).toBeNull()
    expect(kimiPlanText(kimiTranscriptToolFrame({
      toolCallId: 'c',
      name: KIMI_TOOL.ExitPlanMode,
      input: {},
      state: 'done',
      output: '# Plan',
    }))).toBeNull()
  })
})
