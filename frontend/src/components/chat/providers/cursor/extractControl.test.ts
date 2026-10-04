import { describe, expect, it } from 'vitest'
import { cursorExtractControl } from './extractControl'

describe('cursorExtractControl', () => {
  it('shows the native plan text with its name and overview before approval', () => {
    expect(cursorExtractControl({ payload: {
      method: 'cursor/create_plan',
      params: {
        name: 'Review changes',
        overview: 'Review without edits.',
        plan: '# Plan\n\n1. Inspect the files.',
      },
    } })).toEqual({
      kind: 'permission',
      permission: {
        title: 'Create Plan: Review changes',
        reason: 'Review without edits.',
        text: '# Plan\n\n1. Inspect the files.',
        options: [],
      },
    })
  })

  it('keeps an absent plan body absent', () => {
    expect(cursorExtractControl({ payload: { method: 'cursor/create_plan', params: { name: 'Review' } } })).toEqual({
      kind: 'permission',
      permission: { title: 'Create Plan: Review', options: [] },
    })
  })
})
