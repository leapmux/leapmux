import type { Component } from 'solid-js'
import { MemoryRouter, Route } from '@solidjs/router'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { HubPage } from './HubPage'

const ceremoniesInBrowser = vi.hoisted(() => vi.fn<() => boolean>())

vi.mock('~/lib/hubSite', () => ({
  ceremoniesRunInBrowser: () => ceremoniesInBrowser(),
  openHubPage: vi.fn(),
  hubPageUrl: vi.fn(),
  hubOrigin: vi.fn(),
}))

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    loading: () => false,
    refreshUser: vi.fn(),
  }),
}))

// Constructed (not merely rendered): in remote mode the children must never
// even mount, so the marker runs in the component body.
const childConstructed = vi.fn()
function Child() {
  childConstructed()
  return <div data-testid="child">child content</div>
}

const CustomDesktop: Component = () => <div data-testid="custom-desktop" />

/** Renders `page` at `/` inside a real router, so `useNavigate` has a route. */
function renderPage(page: Component) {
  return render(() => (
    <MemoryRouter>
      <Route path="/" component={page} />
    </MemoryRouter>
  ))
}

describe('HubPage', () => {
  beforeEach(() => {
    ceremoniesInBrowser.mockReset()
    childConstructed.mockClear()
  })

  it('renders the children in a browser', () => {
    ceremoniesInBrowser.mockReturnValue(false)

    renderPage(() => (
      <HubPage>
        <Child />
      </HubPage>
    ))

    expect(screen.getByTestId('child')).toBeInTheDocument()
    expect(childConstructed).toHaveBeenCalledTimes(1)
  })

  // In remote-Hub mode the children cannot complete -- passkeys, OAuth and
  // captcha need the Hub's origin -- so the desktop view renders instead and
  // the children never mount.
  it('renders the desktop view instead of the children in remote mode', () => {
    ceremoniesInBrowser.mockReturnValue(true)

    // The default desktop view.
    renderPage(() => (
      <HubPage>
        <Child />
      </HubPage>
    ))
    expect(screen.getByText('Continue in your browser')).toBeInTheDocument()
    expect(childConstructed).not.toHaveBeenCalled()
    cleanup()

    // A custom desktop view replaces the default.
    renderPage(() => (
      <HubPage inDesktop={() => <CustomDesktop />}>
        <Child />
      </HubPage>
    ))
    expect(screen.getByTestId('custom-desktop')).toBeInTheDocument()
    expect(childConstructed).not.toHaveBeenCalled()
  })
})
