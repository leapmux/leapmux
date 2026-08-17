import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'

import { SignupForm } from './SignupForm'

const mockSignUp = vi.fn<(...args: unknown[]) => Promise<unknown>>()

vi.mock('~/api/clients', () => ({
  authClient: {
    signUp: (...args: unknown[]) => mockSignUp(...args),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

// The real captchaForm state reads these; answering "loaded, no captcha"
// keeps blocksSubmit() false so the form renders fully interactive without
// a widget. The CaptchaSection that would render the widget is stubbed
// below precisely because there is no widget to mount.
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => true,
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  refreshSnapshot: () => {},
}))

vi.mock('~/components/common/CaptchaSection', () => ({
  CaptchaSection: () => <div data-testid="captcha-section" />,
}))

function usernameInput() {
  return screen.getByLabelText('Username') as HTMLInputElement
}

function displayNameInput() {
  return screen.getByLabelText('Display Name') as HTMLInputElement
}

function renderForm() {
  return render(() => (
    <SignupForm
      submitLabel="Create account"
      submittingLabel="Creating account..."
      onSuccess={() => {}}
    />
  ))
}

describe('signup form display-name mirror', () => {
  it('mirrors the username into the display name as it is typed', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    expect(displayNameInput().value).toBe('alice')

    // Keeps up with edits to the username, raw casing included: the slug is
    // lowercased at submit, but the display name keeps what the user typed.
    fireEvent.input(usernameInput(), { target: { value: 'Alice-dev' } })
    expect(displayNameInput().value).toBe('Alice-dev')
  })

  it('stops mirroring once the display name is edited directly', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(displayNameInput(), { target: { value: 'Alice Smith' } })
    expect(displayNameInput().value).toBe('Alice Smith')

    // Later username typing must not clobber the user's own name.
    fireEvent.input(usernameInput(), { target: { value: 'bob' } })
    expect(usernameInput().value).toBe('bob')
    expect(displayNameInput().value).toBe('Alice Smith')
  })

  it('does not re-arm the mirror after the display name is cleared', () => {
    renderForm()

    fireEvent.input(usernameInput(), { target: { value: 'alice' } })
    fireEvent.input(displayNameInput(), { target: { value: 'x' } })
    fireEvent.input(displayNameInput(), { target: { value: '' } })

    fireEvent.input(usernameInput(), { target: { value: 'bobby' } })
    expect(displayNameInput().value).toBe('')
  })
})
