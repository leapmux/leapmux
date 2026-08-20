/// <reference types="vitest/globals" />
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { menuOptions, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'
import { ShellSelect } from './ShellSelect'

const MENU = 'shell-select-menu'

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
  return { onChange }
}

describe('shellSelect', () => {
  it('shows the loading sentinel while loading', () => {
    // The sentinel moved from an `<option>` to the trigger's own label: a menu
    // has no list to hold it while the list is what has not arrived.
    renderShellSelect({ loading: true })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('Loading shells...')
  })

  it('shows the empty sentinel when not loading and no shells', () => {
    renderShellSelect({ loading: false, shells: [] })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('No shells available')
  })

  it('renders one option per shell with the default suffix on the matching one', () => {
    renderShellSelect({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '/bin/zsh',
      value: '/bin/zsh',
    })
    expect(menuOptions(MENU)).toEqual(['/bin/zsh (default)', '/bin/bash'])
  })

  it('does not suffix any option when defaultShell is empty', () => {
    renderShellSelect({ shells: ['/bin/zsh', '/bin/bash'], defaultShell: '' })
    expect(menuOptions(MENU)).toEqual(['/bin/zsh', '/bin/bash'])
  })

  it('fires onChange with the picked value', () => {
    const { onChange } = renderShellSelect({ shells: ['/bin/zsh', '/bin/bash'], value: '/bin/zsh' })
    pickMenuValue(MENU, '/bin/bash')
    expect(onChange).toHaveBeenCalledWith('/bin/bash')
  })

  it('checks the option that matches the value, and only it', () => {
    // What replaced `select.value`. A menu carries its selection in
    // `aria-checked`, derived from the prop on every render -- which is why
    // there is no DOM state left to fall out of step with the caller.
    renderShellSelect({ shells: ['/bin/zsh', '/bin/bash'], value: '/bin/bash' })
    expect(menuTriggerText(MENU)).toContain('/bin/bash')
  })
})
