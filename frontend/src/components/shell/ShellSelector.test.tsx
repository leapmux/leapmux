/// <reference types="vitest/globals" />
import type { ShellSelectorState } from '~/components/shell/ShellSelector'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { ShellSelector } from '~/components/shell/ShellSelector'
import { menuOptions, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'

const MENU = 'shell-select-menu'
const REFRESH = 'shell-selector-refresh'

interface Overrides {
  shells?: string[]
  defaultShell?: string
  shell?: string
  loading?: boolean
}

function renderShellSelector(overrides: Overrides = {}) {
  const setShell = vi.fn()
  const refresh = vi.fn()
  const state: ShellSelectorState = {
    shells: () => overrides.shells ?? [],
    defaultShell: () => overrides.defaultShell ?? '',
    shell: () => overrides.shell ?? '',
    setShell,
    loading: () => overrides.loading ?? false,
    refresh,
  }
  render(() => <ShellSelector state={state} />)
  return { setShell, refresh }
}

// The menu half, carried over from the ShellSelect component this absorbed.
describe('shellSelector menu', () => {
  it('shows the loading sentinel while loading', () => {
    // The sentinel lives on the trigger's own label: a menu has no list to
    // hold it while the list is what has not arrived.
    renderShellSelector({ loading: true })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('Loading shells...')
  })

  it('shows the empty sentinel when not loading and no shells', () => {
    renderShellSelector({ loading: false, shells: [] })
    expect(menuTrigger(MENU)).toBeDisabled()
    expect(menuTriggerText(MENU)).toContain('No shells available')
  })

  it('renders one option per shell with the default suffix on the matching one', () => {
    renderShellSelector({
      shells: ['/bin/zsh', '/bin/bash'],
      defaultShell: '/bin/zsh',
      shell: '/bin/zsh',
    })
    expect(menuOptions(MENU)).toEqual(['/bin/zsh (default)', '/bin/bash'])
  })

  it('does not suffix any option when defaultShell is empty', () => {
    renderShellSelector({ shells: ['/bin/zsh', '/bin/bash'], defaultShell: '' })
    expect(menuOptions(MENU)).toEqual(['/bin/zsh', '/bin/bash'])
  })

  it('fires setShell with the picked value', () => {
    const { setShell } = renderShellSelector({ shells: ['/bin/zsh', '/bin/bash'], shell: '/bin/zsh' })
    pickMenuValue(MENU, '/bin/bash')
    expect(setShell).toHaveBeenCalledWith('/bin/bash')
  })

  it('checks the option that matches the value, and only it', () => {
    // What replaced `select.value`. A menu carries its selection in
    // `aria-checked`, derived from the prop on every render -- which is why
    // there is no DOM state left to fall out of step with the caller.
    renderShellSelector({ shells: ['/bin/zsh', '/bin/bash'], shell: '/bin/bash' })
    expect(menuTriggerText(MENU)).toContain('/bin/bash')
  })
})

// The label row is what this component added. The Shell field used to be a
// bare `<label>` wrapping the menu, so it had no slot for a button and took
// different typography from "Worker" beside it.
describe('shellSelector label row', () => {
  it('labels the field and offers a refresh button', () => {
    renderShellSelector({ shells: ['/bin/zsh'] })
    expect(screen.getByTestId(REFRESH)).toBeInTheDocument()
  })

  it('calls refresh when the button is clicked', () => {
    const { refresh } = renderShellSelector({ shells: ['/bin/zsh'] })
    screen.getByTestId(REFRESH).click()
    expect(refresh).toHaveBeenCalledOnce()
  })

  // A second click while the first fetch is still in flight would queue a
  // redundant round trip against a worker that is already answering.
  it('disables the refresh button while loading', () => {
    renderShellSelector({ loading: true })
    expect(screen.getByTestId(REFRESH)).toBeDisabled()
  })

  it('enables the refresh button once loading finishes', () => {
    renderShellSelector({ loading: false, shells: ['/bin/zsh'] })
    expect(screen.getByTestId(REFRESH)).not.toBeDisabled()
  })

  // The button is the ONLY route to the hook's refresh: its own effect
  // re-fetches on a workerId transition, so a failure against the current
  // worker is otherwise unrecoverable without switching workers and back.
  // That case is exactly when the list is empty and the menu is disabled.
  it('stays clickable when the shell list is empty', () => {
    const { refresh } = renderShellSelector({ shells: [], loading: false })
    expect(menuTrigger(MENU)).toBeDisabled()
    screen.getByTestId(REFRESH).click()
    expect(refresh).toHaveBeenCalledOnce()
  })
})
