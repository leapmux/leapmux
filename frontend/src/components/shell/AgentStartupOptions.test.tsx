import type { StartupOptionGroup } from '../chat/providers/capabilities'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStartupOptions } from './AgentStartupOptions'

const groups: StartupOptionGroup[] = [{
  id: 'trust',
  label: 'Workspace trust',
  defaultValue: 'native',
  readOnlyReason: 'Choose before creation',
  options: [{ value: 'native', label: 'Use native trust' }, { value: 'agent', label: 'Trust for this agent' }],
}]

function mount(disabled = false) {
  const [selected, setSelected] = createSignal<Record<string, string>>({})
  render(() => <AgentStartupOptions groups={groups} selected={selected()} onChange={(id, value) => setSelected({ [id]: value })} disabled={disabled} />)
  return selected
}

describe('AgentStartupOptions', () => {
  it('shows the default and returns the selected value', async () => {
    const selected = mount()
    expect(screen.getByRole('radio', { name: 'Use native trust' })).toHaveAttribute('aria-checked', 'true')
    await fireEvent.click(screen.getByRole('radio', { name: 'Trust for this agent' }))
    expect(selected()).toEqual({ trust: 'agent' })
  })

  it('uses the shared radio keyboard contract', async () => {
    const selected = mount()
    const first = screen.getByRole('radio', { name: 'Use native trust' })
    first.focus()
    await fireEvent.keyDown(first, { key: 'ArrowRight' })
    expect(selected()).toEqual({ trust: 'agent' })
  })

  it('refuses selection while disabled', async () => {
    const selected = mount(true)
    const choice = screen.getByRole('radio', { name: 'Trust for this agent' })
    expect(choice).toBeDisabled()
    await fireEvent.click(choice)
    expect(selected()).toEqual({})
  })

  it('renders no controls for an absent startup axis', () => {
    render(() => <AgentStartupOptions groups={[]} selected={{}} onChange={() => {}} />)
    expect(screen.queryByRole('radiogroup')).toBeNull()
  })
})
