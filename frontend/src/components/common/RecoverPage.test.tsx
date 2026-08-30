import { Code, ConnectError } from '@connectrpc/connect'
import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CaptchaProvider } from '~/generated/proto/leapmux/v1/auth_pb'
import { mockLoadSystemInfo, resetSystemInfoMock, setSystemInfoMock } from '~/test-support/systemInfoMock'

import { RecoverPage } from './RecoverPage'

const mockRequestAccountRecovery = vi.fn()

vi.mock('~/api/clients', () => ({
  authClient: {
    requestAccountRecovery: (...args: unknown[]) => mockRequestAccountRecovery(...args),
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

function renderRecoverPage() {
  const history = createMemoryHistory()
  history.set({ value: '/recover-account', replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/recover-account" component={RecoverPage} />
      <Route path="/login" component={() => <div data-testid="login-page" />} />
    </MemoryRouter>
  ))
}

describe('recover page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetSystemInfoMock()
    setMockCaptchaPayload(null)
    mockRequestAccountRecovery.mockResolvedValue({})
  })

  it('keeps Send recovery link disabled until an identifier is entered', async () => {
    renderRecoverPage()
    expect(await screen.findByRole('button', { name: 'Send recovery link' })).toBeDisabled()
    fireEvent.input(screen.getByLabelText('Email or username'), { target: { value: 'alice' } })
    expect(screen.getByRole('button', { name: 'Send recovery link' })).toBeEnabled()
  })

  it('shows the generic success copy and does not re-enable submit after a successful request', async () => {
    renderRecoverPage()
    fireEvent.input(await screen.findByLabelText('Email or username'), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send recovery link' }))
    await vi.waitFor(() => {
      expect(mockRequestAccountRecovery).toHaveBeenCalledWith(expect.objectContaining({
        identifier: 'alice',
      }))
    })
    expect(await screen.findByText(/If an account with that email or username exists/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send recovery link' })).not.toBeInTheDocument()
  })

  it('passes the account_recovery action to the captcha field', async () => {
    setSystemInfoMock({ captchaEnabled: true, captchaProvider: CaptchaProvider.TURNSTILE, captchaSiteKey: '1x00000000000000000000AA' })
    renderRecoverPage()
    await vi.waitFor(() => {
      expect(screen.getByTestId('captcha-field')).toHaveAttribute('data-action', 'account_recovery')
    })
  })

  it('re-enables Send recovery link after a failed attempt and refreshes captcha info on PermissionDenied', async () => {
    mockRequestAccountRecovery.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied))
    renderRecoverPage()
    fireEvent.input(await screen.findByLabelText('Email or username'), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send recovery link' }))
    await vi.waitFor(() => {
      expect(mockRequestAccountRecovery).toHaveBeenCalledOnce()
    })
    expect(await screen.findByRole('button', { name: 'Send recovery link' })).toBeEnabled()
    expect(mockLoadSystemInfo).toHaveBeenCalledWith(true)
  })
})
