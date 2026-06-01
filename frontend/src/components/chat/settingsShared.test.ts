import type { AvailableModel } from '~/generated/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { effortValidForModel, effortValueForModel } from './settingsShared'

/** Build a minimal AvailableModel fixture (only the fields the helper reads). */
function model(id: string, effortIds: string[]): AvailableModel {
  return {
    id,
    supportedEfforts: effortIds.map(eid => ({ id: eid })),
  } as unknown as AvailableModel
}

const models = [
  model('opus', ['auto', 'ultracode', 'max', 'xhigh', 'high', 'medium', 'low']),
  model('sonnet', ['auto', 'max', 'high', 'medium', 'low']),
  model('haiku', []),
]

describe('effortValidForModel', () => {
  it('returns true when the effort is one of the model tiers', () => {
    expect(effortValidForModel(models, 'opus', 'ultracode')).toBe(true)
    expect(effortValidForModel(models, 'sonnet', 'max')).toBe(true)
  })

  it('returns false when the effort is not offered by the model', () => {
    // "ultracode"/"xhigh" left over from Opus after switching to Sonnet.
    expect(effortValidForModel(models, 'sonnet', 'ultracode')).toBe(false)
    expect(effortValidForModel(models, 'sonnet', 'xhigh')).toBe(false)
  })

  it('returns false for a model that offers no efforts', () => {
    expect(effortValidForModel(models, 'haiku', 'auto')).toBe(false)
  })

  it('returns false for an unknown model id', () => {
    expect(effortValidForModel(models, 'gpt-not-a-model', 'auto')).toBe(false)
  })

  it('returns false when the model list is empty or undefined', () => {
    expect(effortValidForModel(undefined, 'opus', 'auto')).toBe(false)
    expect(effortValidForModel([], 'opus', 'auto')).toBe(false)
  })
})

describe('effortValueForModel', () => {
  it('returns the current effort when the model still offers it', () => {
    expect(effortValueForModel(models, 'opus', 'ultracode')).toBe('ultracode')
    expect(effortValueForModel(models, 'sonnet', 'max')).toBe('max')
  })

  it('falls back to auto when the effort is not offered by the model', () => {
    // The exact optimistic-switch case: "ultracode"/"xhigh" left over from Opus
    // after switching to Sonnet must not stay selected in the RadioGroup.
    expect(effortValueForModel(models, 'sonnet', 'ultracode')).toBe('auto')
    expect(effortValueForModel(models, 'sonnet', 'xhigh')).toBe('auto')
  })

  it('falls back to auto for an unknown model or empty list', () => {
    expect(effortValueForModel(models, 'gpt-not-a-model', 'high')).toBe('auto')
    expect(effortValueForModel(undefined, 'opus', 'ultracode')).toBe('auto')
    expect(effortValueForModel([], 'opus', 'ultracode')).toBe('auto')
  })
})
