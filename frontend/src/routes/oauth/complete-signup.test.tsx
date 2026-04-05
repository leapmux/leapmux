import type { MemoryHistory } from '@solidjs/router'

import { createMemoryHistory, MemoryRouter, Route } from '@solidjs/router'
/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import OAuthCompleteSignupPage from './complete-signup'

const mockGetPendingOAuthSignup = vi.fn<(...args: unknown[]) => Promise<Record<string, unknown>>>()
const mockCompleteOAuthSignup = vi.fn<(...args: unknown[]) => Promise<Record<string, unknown>>>()

vi.mock('~/api/clients', () => ({
  authClient: {
    getPendingOAuthSignup: (...args: unknown[]) => mockGetPendingOAuthSignup(...args),
    completeOAuthSignup: (...args: unknown[]) => mockCompleteOAuthSignup(...args),
    login: vi.fn(),
    logout: vi.fn(),
    getCurrentUser: vi.fn(),
  },
}))

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: () => Promise.resolve(),
  isSignupEnabled: () => false,
  loadOAuthProviders: () => Promise.resolve([]),
}))

const mockSetAuth = vi.fn()
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => null,
    loading: () => false,
    error: () => null,
    login: vi.fn(),
    logout: vi.fn(),
    setAuth: mockSetAuth,
    isAuthenticated: () => false,
  }),
  AuthProvider: (props: { children: unknown }) => <>{props.children}</>,
}))

function createHistoryAt(url: string): MemoryHistory {
  const history = createMemoryHistory()
  history.set({ value: url, scroll: false, replace: true })
  return history
}

function renderPage(token = 'test-token') {
  const history = createHistoryAt(`/?token=${token}`)
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/" component={OAuthCompleteSignupPage} />
    </MemoryRouter>
  ))
}

describe('oAuthCompleteSignupPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders form with pre-filled display name and email', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Display Name')).toBeInTheDocument()
    })

    const displayNameInput = screen.getByLabelText('Display Name') as HTMLInputElement
    const emailInput = screen.getByLabelText('Email') as HTMLInputElement
    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement

    expect(displayNameInput.value).toBe('Test User')
    expect(emailInput.value).toBe('test@example.com')
    expect(usernameInput.value).toBe('')
    expect(screen.getByText(/Signed in via/)).toBeInTheDocument()
    expect(screen.getByText(/GitHub/)).toBeInTheDocument()
  })

  it('shows error when token is invalid', async () => {
    mockGetPendingOAuthSignup.mockRejectedValue(new Error('invalid token'))

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByText('This signup link is invalid or has expired.')).toBeInTheDocument()
    })
  })

  it('calls completeOAuthSignup on submit', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockResolvedValue({
      user: { id: 'u1', username: 'testuser' },
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement
    fireEvent.input(usernameInput, { target: { value: 'testuser' } })

    const submitButton = screen.getByRole('button', { name: 'Create account' })
    fireEvent.click(submitButton)

    await vi.waitFor(() => {
      expect(mockCompleteOAuthSignup).toHaveBeenCalledWith({
        signupToken: 'test-token',
        username: 'testuser',
        displayName: 'Test User',
      })
    })

    expect(mockSetAuth).toHaveBeenCalledWith({ id: 'u1', username: 'testuser' })
  })

  it('shows email as read-only when provider supplies it', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'provider@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Email')).toBeInTheDocument()
    })

    const emailInput = screen.getByLabelText('Email') as HTMLInputElement
    expect(emailInput.value).toBe('provider@example.com')
    expect(emailInput.readOnly).toBe(true)
  })

  it('shows error for username taken', async () => {
    mockGetPendingOAuthSignup.mockResolvedValue({
      email: 'test@example.com',
      displayName: 'Test User',
      providerName: 'GitHub',
    })
    mockCompleteOAuthSignup.mockRejectedValue(new Error('username already taken'))

    renderPage()

    await vi.waitFor(() => {
      expect(screen.getByLabelText('Username')).toBeInTheDocument()
    })

    const usernameInput = screen.getByLabelText('Username') as HTMLInputElement
    fireEvent.input(usernameInput, { target: { value: 'taken-user' } })

    const submitButton = screen.getByRole('button', { name: 'Create account' })
    fireEvent.click(submitButton)

    await vi.waitFor(() => {
      expect(screen.getByText('username already taken')).toBeInTheDocument()
    })
  })
})
