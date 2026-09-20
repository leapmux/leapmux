import { describe, expect, it } from 'vitest'
import { codexPlanItemMarkdown, codexTurnPlanTodos } from './plan'

describe('codexTurnPlanTodos', () => {
  it('returns null for missing params', () => {
    expect(codexTurnPlanTodos(null)).toBeNull()
    expect(codexTurnPlanTodos(undefined)).toBeNull()
  })

  it('returns null when plan is not an array', () => {
    expect(codexTurnPlanTodos({ plan: 'oops' })).toBeNull()
  })

  // An empty ARRAY is a cleared plan, which the shared checklist header states in
  // its own words. Null is the different answer: no plan at all.
  it('returns an empty list for a cleared plan', () => {
    expect(codexTurnPlanTodos({ plan: [] })).toEqual([])
  })

  it('maps Codex statuses (inProgress → in_progress, completed → completed, default → pending)', () => {
    const source = codexTurnPlanTodos({
      plan: [
        { step: 'one', status: 'pending' },
        { step: 'two', status: 'inProgress' },
        { step: 'three', status: 'completed' },
      ],
    })
    expect(source).toEqual([
      { rowKey: '0:one', content: 'one', status: 'pending', activeForm: 'one' },
      { rowKey: '1:two', content: 'two', status: 'in_progress', activeForm: 'two' },
      { rowKey: '2:three', content: 'three', status: 'completed', activeForm: 'three' },
    ])
  })

  it('skips entries without a step', () => {
    expect(codexTurnPlanTodos({ plan: [{ step: 'a' }, {}, null] })).toHaveLength(1)
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
