/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ShellSelect } from './ShellSelect'

function renderShellSelect(overrides: Partial<Parameters<typeof ShellSelect>[0]> = {}) {
  const onChange = vi.fn()
  const props = {
    value: '',
    onChange,
    shells: [] as string[],
    defaultShell: '',
    loading: false,
    ...overrides,
  }
  render(() => <ShellSelect {...props} />)
  const select = screen.getByRole('combobox') as HTMLSelectElement
  return { select, onChange }
}

describe('shellSelect', () => {
  it('shows the loading sentinel option while loading', () => {
    const { select } = renderShellSelect({ loading: true })
    expect(select.disabled).toBe(true)
    expect(select.value).toBe('')
    expect(Array.from(select.options).map(o => o.textContent)).toContain('Loading shells...')
  })

  it('shows the empty sentinel option when not loading and no shells', () => {
    const { select } = renderShellSelect({ loading: false, shells: [] })
    expect(select.disabled).toBe(true)
    expect(Array.from(select.options).map(o => o.textContent)).toContain('No shells available')
  })

  it('renders one option per shell with the default suffix on the matching one', () => {
    const { select } = renderShellSelect({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '/bin/zsh',
      value: '/bin/zsh',
    })
    expect(select.disabled).toBe(false)
    const labels = Array.from(select.options).map(o => o.textContent)
    expect(labels).toEqual(['/bin/zsh (default)', '/bin/bash'])
  })

  it('does not suffix any option when defaultShell is empty', () => {
    const { select } = renderShellSelect({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '',
      value: '/bin/zsh',
    })
    const labels = Array.from(select.options).map(o => o.textContent)
    expect(labels).toEqual(['/bin/zsh', '/bin/bash'])
  })

  it('fires onChange with the picked value', () => {
    const { select, onChange } = renderShellSelect({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '/bin/zsh',
      value: '/bin/zsh',
    })
    fireEvent.change(select, { target: { value: '/bin/bash' } })
    expect(onChange).toHaveBeenCalledWith('/bin/bash')
  })
})
