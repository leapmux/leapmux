import type { AvailableModel } from '~/generated/leapmux/v1/agent_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { effortValidForModel, effortValueForModel, RadioGroup } from './settingsShared'

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

const radioItems = [
  { label: 'Low', value: 'low' },
  { label: 'High', value: 'high' },
]

describe('radioGroup', () => {
  it('is writable by default: no data-disabled, enabled inputs, click fires onChange', async () => {
    const onChange = vi.fn()
    const { container } = render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'low',
      onChange,
    }))

    const group = container.querySelector('[role="group"]')!
    expect(group.hasAttribute('data-disabled')).toBe(false)
    expect(group.hasAttribute('aria-disabled')).toBe(false)

    const highInput = screen.getByTestId('tl-high').querySelector('input')!
    expect(highInput).not.toBeDisabled()
    await fireEvent.click(highInput)
    expect(onChange).toHaveBeenCalledWith('high')
  })

  it('when disabled: marks the group, disables inputs, keeps the current value checked, and ignores clicks', async () => {
    const onChange = vi.fn()
    render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'high',
      onChange,
      disabled: true,
    }))

    const group = screen.getByRole('group')
    expect(group).toHaveAttribute('data-disabled')
    expect(group).toHaveAttribute('aria-disabled', 'true')

    const highInput = screen.getByTestId('tl-high').querySelector('input')!
    const lowInput = screen.getByTestId('tl-low').querySelector('input')!
    expect(highInput).toBeDisabled()
    expect(lowInput).toBeDisabled()
    expect(highInput).toBeChecked() // current value stays selected

    await fireEvent.click(lowInput)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('wraps the group in a tooltip when disabledReason is set', () => {
    const { container } = render(() => RadioGroup({
      label: 'Effort',
      items: radioItems,
      testIdPrefix: 'tl',
      name: 'tl',
      current: 'high',
      onChange: () => {},
      disabled: true,
      disabledReason: 'Controlled by the agent',
    }))

    // The Tooltip wraps its children in a display:contents span; the disabled group
    // sits directly inside it, confirming the reason is wired (not silently dropped).
    const group = container.querySelector('[role="group"]')!
    const wrapper = group.parentElement!
    expect(wrapper.tagName).toBe('SPAN')
    expect(wrapper.getAttribute('style')).toContain('display:contents')
  })
})
