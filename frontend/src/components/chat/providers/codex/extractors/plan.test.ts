import { describe, expect, it } from 'vitest'
import { codexPlanItemMarkdown, codexTurnPlanFromParams } from './plan'

describe('codexTurnPlanFromParams', () => {
  it('returns null for missing params', () => {
    expect(codexTurnPlanFromParams(null)).toBeNull()
    expect(codexTurnPlanFromParams(undefined)).toBeNull()
  })

  it('returns null when plan is not an array', () => {
    expect(codexTurnPlanFromParams({ plan: 'oops' })).toBeNull()
  })

  it('returns the cleared placeholder when plan is empty', () => {
    expect(codexTurnPlanFromParams({ plan: [] })).toEqual({
      toolName: 'Plan Update',
      title: '',
      todos: [],
    })
  })

  it('maps Codex statuses (inProgress → in_progress, completed → completed, default → pending)', () => {
    const source = codexTurnPlanFromParams({
      plan: [
        { step: 'one', status: 'pending' },
        { step: 'two', status: 'inProgress' },
        { step: 'three', status: 'completed' },
      ],
    })
    expect(source?.todos).toEqual([
      { content: 'one', status: 'pending', activeForm: 'one' },
      { content: 'two', status: 'in_progress', activeForm: 'two' },
      { content: 'three', status: 'completed', activeForm: 'three' },
    ])
    expect(source?.title).toBe('3 tasks')
  })

  it('appends the trimmed explanation to the title', () => {
    const source = codexTurnPlanFromParams({
      plan: [{ step: 'a' }],
      explanation: '  fix login bug  ',
    })
    expect(source?.title).toBe('1 task - fix login bug')
  })

  it('skips entries without a step', () => {
    const source = codexTurnPlanFromParams({
      plan: [{ step: 'a' }, {}, null],
    })
    expect(source?.todos).toHaveLength(1)
  })
})

describe('codexPlanItemMarkdown', () => {
  it('returns null when item is missing or wrong type', () => {
    expect(codexPlanItemMarkdown(null)).toBeNull()
    expect(codexPlanItemMarkdown(undefined)).toBeNull()
    expect(codexPlanItemMarkdown({ type: 'agentMessage', text: 'x' })).toBeNull()
  })

  it('returns null when text is missing or empty', () => {
    expect(codexPlanItemMarkdown({ type: 'plan' })).toBeNull()
    expect(codexPlanItemMarkdown({ type: 'plan', text: '' })).toBeNull()
  })

  it('returns the text body verbatim', () => {
    expect(codexPlanItemMarkdown({ type: 'plan', text: '# Plan\n\n- step' })).toBe('# Plan\n\n- step')
  })
})
