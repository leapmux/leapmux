import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { ForgotPasswordPage } from './ForgotPasswordPage'

const mockRequestPasswordReset = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    requestPasswordReset: (...args: unknown[]) => mockRequestPasswordReset(...args),
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

function renderForgotPasswordPage() {
  const history = createMemoryHistory()
  history.set({ value: '/forgot-password', replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/forgot-password" component={ForgotPasswordPage} />
      <Route path="/login" component={() => <div data-testid="login-page" />} />
    </MemoryRouter>
  ))
}

describe('forgot password page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockRequestPasswordReset.mockResolvedValue({})
  })

  it('keeps Send reset link disabled until an identifier is entered', async () => {
    renderForgotPasswordPage()
    expect(await screen.findByRole('button', { name: 'Send reset link' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Email or username'), { target: { value: 'alice' } })
    expect(screen.getByRole('button', { name: 'Send reset link' })).toBeEnabled()
  })

  it('shows the generic success copy and does not re-enable submit after a successful request', async () => {
    renderForgotPasswordPage()
    fireEvent.input(await screen.findByLabelText('Email or username'), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send reset link' }))
    await vi.waitFor(() => {
      expect(mockRequestPasswordReset).toHaveBeenCalledWith(expect.objectContaining({
        identifier: 'alice',
      }))
    })
    expect(await screen.findByText(/If an account with that email or username exists/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send reset link' })).not.toBeInTheDocument()
  })

  it('passes the password_reset action to the captcha field', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderForgotPasswordPage()
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'password_reset')
    })
  })

  it('re-enables Send reset link after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockRequestPasswordReset.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderForgotPasswordPage()
    fireEvent.input(await screen.findByLabelText('Email or username'), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send reset link' }))
    await vi.waitFor(() => {
      expect(mockRequestPasswordReset).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Send reset link' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})
