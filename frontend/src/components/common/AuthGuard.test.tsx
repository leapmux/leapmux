import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AuthGuard } from '~/components/common/AuthGuard'

// Mock the auth context module
const mockUser = vi.fn()
const mockLoading = vi.fn()
const mockIsAuthenticated = vi.fn()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: mockUser,
    loading: mockLoading,
    isAuthenticated: mockIsAuthenticated,
    error: () => null,
    login: async () => {},
    logout: async () => {},
    setAuth: () => {},
  }),
}))

// Mock the router primitives
vi.mock('@solidjs/router', () => ({
  useLocation: () => ({ pathname: '/test', search: '' }),
  useNavigate: () => vi.fn(),
  A: (props: any) => <a href={props.href}>{props.children}</a>,
}))

describe('authGuard', () => {
  it('renders children for authenticated non-admin user (no requireAdmin)', () => {
    mockUser.mockReturnValue({ id: '1', isAdmin: false })
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(true)

    render(() => (
      <AuthGuard>
        <div>Protected Content</div>
      </AuthGuard>
    ))

    expect(screen.getByText('Protected Content')).toBeInTheDocument()
  })

  it('renders children for admin when requireAdmin is true', () => {
    mockUser.mockReturnValue({ id: '1', isAdmin: true })
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(true)

    render(() => (
      <AuthGuard requireAdmin>
        <div>Admin Content</div>
      </AuthGuard>
    ))

    expect(screen.getByText('Admin Content')).toBeInTheDocument()
  })

  it('shows NotFoundPage for non-admin when requireAdmin is true', () => {
    mockUser.mockReturnValue({ id: '1', isAdmin: false })
    mockLoading.mockReturnValue(false)
    mockIsAuthenticated.mockReturnValue(true)

    render(() => (
      <AuthGuard requireAdmin>
        <div>Admin Content</div>
      </AuthGuard>
    ))

    // Should not show admin content
    expect(screen.queryByText('Admin Content')).not.toBeInTheDocument()
    // Should show 404 page
    expect(screen.getByText('Not Found')).toBeInTheDocument()
  })

  it('shows loading while auth is loading', () => {
    mockUser.mockReturnValue(null)
    mockLoading.mockReturnValue(true)
    mockIsAuthenticated.mockReturnValue(false)

    render(() => (
      <AuthGuard>
        <div>Protected Content</div>
      </AuthGuard>
    ))

    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument()
  })
})
