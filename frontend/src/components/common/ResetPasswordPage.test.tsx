import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/leapmux/v1/auth_pb'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { ResetPasswordPage } from './ResetPasswordPage'

const mockCompletePasswordReset = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    completePasswordReset: (...args: unknown[]) => mockCompletePasswordReset(...args),
  },
}))

vi.mock('~/lib/systemInfo', async () => {
  const m = await import('~/test-support/systemInfoMock')
  return m.systemInfoMock
})

const [mockCaptchaPayload, setMockCaptchaPayload] = createSignal<string | null>(null)
vi.mock('~/components/common/CaptchaField', async () => {
  const { createEffect } = await import('solid-js')
  return {
    CaptchaField: (props: { action: string, onPayload: (p: string | null) => void, onUnavailable: () => void }) => {
      createEffect(() => props.onPayload(mockCaptchaPayload()))
      return <div data-testid="captcha-field" data-action={props.action} />
    },
  }
})
vi.mock('~/components/common/CaptchaHoneypot', () => ({
  CaptchaHoneypot: (props: { value: string, onInput: (v: string) => void }) => (
    <input
      data-testid="captcha-honeypot"
      type="text"
      name="website"
      value={props.value}
      onInput={e => props.onInput(e.currentTarget.value)}
    />
  ),
}))

function renderResetPasswordPage(initialPath: string) {
  const history = createMemoryHistory()
  history.set({ value: initialPath, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/reset-password" component={ResetPasswordPage} />
      <Route path="/login" component={() => <div data-testid="login-page" />} />
      <Route path="/forgot-password" component={() => <div data-testid="forgot-page" />} />
    </MemoryRouter>
  ))
}

describe('reset password page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockCompletePasswordReset.mockResolvedValue({})
  })

  it('shows a missing-token error when the reset link has no token', async () => {
    renderResetPasswordPage('/reset-password')
    expect(await screen.findByText('Missing reset token. Open the link from your email.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Request a new link' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reset password' })).not.toBeInTheDocument()
  })

  it('keeps Reset password disabled until the new password fields match', async () => {
    renderResetPasswordPage('/reset-password?token=reset-token')
    expect(await screen.findByRole('button', { name: 'Reset password' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('New Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Reset password' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    expect(screen.getByRole('button', { name: 'Reset password' })).toBeEnabled()
  })

  it('navigates to login after a successful reset and does not re-enable submit', async () => {
    let resolveReset!: (value: unknown) => void
    mockCompletePasswordReset.mockReturnValue(new Promise((resolve) => {
      resolveReset = resolve
    }))
    renderResetPasswordPage('/reset-password?token=reset-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Reset password' }))
    expect(await screen.findByRole('button', { name: 'Resetting…' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Resetting…' }))
    expect(mockCompletePasswordReset).toHaveBeenCalledOnce()
    resolveReset({})
    expect(await screen.findByTestId('login-page')).toBeInTheDocument()
  })

  it('passes the complete_password_reset action to the captcha field', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderResetPasswordPage('/reset-password?token=reset-token')
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'complete_password_reset')
    })
  })

  it('re-enables Reset password after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockCompletePasswordReset.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderResetPasswordPage('/reset-password?token=reset-token')
    fireEvent.input(await screen.findByLabelText('New Password'), { target: { value: 'newpass123' } })
    fireEvent.input(screen.getByLabelText('Confirm Password'), { target: { value: 'newpass123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Reset password' }))
    await vi.waitFor(() => {
      expect(mockCompletePasswordReset).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Reset password' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})
