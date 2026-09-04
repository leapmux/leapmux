import { cleanup, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createAuthMethodSelection } from '~/lib/authMethodSelection'
import { AuthMethodPillGroup } from './AuthMethodPillGroup'

vi.mock('~/lib/systemInfo', async () => {
  const mock = await import('~/test-support/systemInfoMock')
  return mock.systemInfoMock
})

const { resetSystemInfoMock, setSystemInfoMock } = await import('~/test-support/systemInfoMock')

function renderGroup() {
  const selection = createAuthMethodSelection('login')
  render(() => <AuthMethodPillGroup label="Sign-in method" selection={selection} />)
}

beforeEach(() => resetSystemInfoMock())
afterEach(() => cleanup())

describe('auth method pill group (AuthMethodPillGroup)', () => {
  it('offers both methods when a ceremony can run', () => {
    setSystemInfoMock({ passkeyBlocker: null })
    renderGroup()
    expect(screen.getAllByRole('radio').map(radio => radio.textContent))
      .toEqual(['Password', 'Passkey'])
  })

  it.each([
    ['insecure-context' as const, /secure page/i],
    ['no-webauthn' as const, /does not support passkeys/i],
  ])('keeps a refused passkey and explains %s', (blocker, expected) => {
    setSystemInfoMock({ passkeyBlocker: blocker })
    renderGroup()
    const passkey = screen.getByRole('radio', { name: 'Passkey' })
    expect(passkey).toHaveAttribute('aria-disabled', 'true')
    expect(passkey).not.toBeDisabled()
    const descriptionId = passkey.getAttribute('aria-describedby')
    expect(descriptionId).toBeTruthy()
    expect(document.getElementById(descriptionId!)?.textContent).toMatch(expected)
  })

  it('drops passkey when the hub does not serve this origin', () => {
    setSystemInfoMock({ passkeyBlocker: 'origin-not-allowed' })
    renderGroup()
    expect(screen.getAllByRole('radio').map(radio => radio.textContent)).toEqual(['Password'])
  })
})
