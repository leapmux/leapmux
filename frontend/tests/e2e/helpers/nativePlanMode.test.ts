/**
 * The unit tests check the instruction comparison of the Plan mode proof, and the order of its steps against fakes.
 * The plan-mode browser specs of each provider check the actual agents.
 */
import type { Page } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { addedInstructionLines, exerciseNativePlanInstructions } from './nativePlanMode'

const fake = vi.hoisted(() => ({ events: [] as string[], instructions: [] as string[], agents: [] as unknown[] }))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  waitForNativeSettingsHydrated: async () => { fake.events.push('hydrated') },
  chooseSettingsOption: async (_page: unknown, testId: string) => { fake.events.push(`choose ${testId}`) },
  waitForSettingsIdle: async () => { fake.events.push('settings idle') },
  expectSettingsOptionChosen: async (_page: unknown, testId: string) => { fake.events.push(`chosen ${testId}`) },
}))
vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: async (_context: unknown, _prompt: string, answer: string) => {
    fake.events.push(`answer ${answer}`)
    return { instructions: fake.instructions.shift() }
  },
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => {
    fake.events.push('agent')
    return fake.agents.shift()
  },
  nativeModelInstructionText: (request: { instructions: string }) => request.instructions,
}))

beforeEach(() => {
  fake.events = []
  fake.instructions = []
  fake.agents = []
})

describe('addedInstructionLines', () => {
  it('returns the trimmed lines that only the later text holds, in order', () => {
    expect(addedInstructionLines('System rules.\nAnswer briefly.', '  System rules.\nYou are in plan mode.\r\nAnswer briefly.\nStay read-only. ')).toBe('You are in plan mode.\nStay read-only.')
  })

  it('returns an empty text when the later text adds nothing', () => {
    expect(addedInstructionLines('A.\nB.', 'B.\n\n  A.  ')).toBe('')
  })

  it('treats every line of the later text as added when the earlier text is empty', () => {
    expect(addedInstructionLines('', 'Plan first.\n\nThen act.')).toBe('Plan first.\nThen act.')
  })
})

function planContext(): ManagedNativeScenarioContext {
  const page = Object.assign({} as Page, {
    reload: async () => {
      fake.events.push('reload')
      return null
    },
  })
  return { page, provider: 0, workspaceId: 'workspace', leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' } } as unknown as ManagedNativeScenarioContext
}

describe('exerciseNativePlanInstructions', () => {
  const before = { id: 'before' } as unknown as AgentInfo
  const after = { id: 'after' } as unknown as AgentInfo

  it('compares a Default request with a Plan request after a reload, and passes both agents to the caller', async () => {
    fake.instructions = ['System rules.', 'System rules.\nYou are in plan mode.']
    fake.agents = [before, after]
    const kept = vi.fn()
    await exerciseNativePlanInstructions(planContext(), { keptSettings: kept })
    expect(kept).toHaveBeenCalledWith(before, after)
    expect(fake.events).toEqual([
      'hydrated',
      'choose permissionMode-default',
      'settings idle',
      'agent',
      'answer DEFAULT_MODE_REPLY',
      'choose permissionMode-plan',
      'settings idle',
      'chosen permissionMode-plan',
      'reload',
      'hydrated',
      'chosen permissionMode-plan',
      'answer PLAN_MODE_REPLY',
      'agent',
    ])
  })

  it.each([
    { label: 'adds no line', planned: 'System rules.' },
    { label: 'adds a line with no plan instruction', planned: 'System rules.\nAnswer in English.' },
  ])('fails when the Plan request $label', async ({ planned }) => {
    fake.instructions = ['System rules.', planned]
    fake.agents = [before, after]
    const kept = vi.fn()
    await expect(exerciseNativePlanInstructions(planContext(), { keptSettings: kept })).rejects.toThrow('planning instructions')
    expect(kept).not.toHaveBeenCalled()
  })

  it('does not count a plan word that the Default request already held', async () => {
    fake.instructions = ['You can plan.', 'You can plan.\nAnswer in English.']
    fake.agents = [before, after]
    await expect(exerciseNativePlanInstructions(planContext(), { keptSettings: () => {} })).rejects.toThrow('planning instructions')
  })
})
