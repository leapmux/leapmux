import type { User } from '~/generated/leapmux/v1/auth_pb'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { OrgProvider, useOrg } from '~/context/OrgContext'

// Mock AuthContext to return a controlled user
let mockUser: Pick<User, 'orgId' | 'orgName'> | null = null
vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    user: () => mockUser,
  }),
}))

// Mock useParams to return a controlled orgSlug
let mockOrgSlug = 'test-org'
vi.mock('@solidjs/router', () => ({
  useParams: () => ({ orgSlug: mockOrgSlug }),
}))

// Test helper component that captures org state
function OrgStateCapture(props: { onState: (state: ReturnType<typeof useOrg>) => void }) {
  const org = useOrg()
  props.onState(org) // eslint-disable-line solid/reactivity -- test helper captures state once
  return <div data-testid="capture">captured</div>
}

function renderOrgState(): ReturnType<typeof useOrg> {
  let capturedState: ReturnType<typeof useOrg> | null = null
  render(() => (
    <OrgProvider>
      <OrgStateCapture onState={(s) => { capturedState = s }} />
    </OrgProvider>
  ))
  expect(capturedState).not.toBeNull()
  return capturedState!
}

describe('orgContext', () => {
  it('derives orgId and slug from the auth user and route param', () => {
    mockOrgSlug = 'alice'
    mockUser = { orgId: 'org-1', orgName: 'alice' }

    const state = renderOrgState()

    expect(state.slug()).toBe('alice')
    expect(state.orgId()).toBe('org-1')
  })

  it('notFound is true when slug does not match the user org', () => {
    mockOrgSlug = 'someone-else'
    mockUser = { orgId: 'org-1', orgName: 'alice' }

    const state = renderOrgState()

    expect(state.notFound()).toBe(true)
  })

  it('notFound is false when slug matches the user org', () => {
    mockOrgSlug = 'alice'
    mockUser = { orgId: 'org-1', orgName: 'alice' }

    const state = renderOrgState()

    expect(state.notFound()).toBe(false)
  })

  it('notFound is false when the slug matches the org case-insensitively', () => {
    // orgName mirrors the store-normalized (lowercased) username, but the slug
    // is the verbatim URL param: a user hand-typing or bookmarking their own org
    // URL with any capital must not be shown a not-found page for their own org.
    mockOrgSlug = 'Alice'
    mockUser = { orgId: 'org-1', orgName: 'alice' }

    const state = renderOrgState()

    expect(state.notFound()).toBe(false)
  })

  it('notFound is false while the user is not loaded', () => {
    mockOrgSlug = 'any-org'
    mockUser = null

    const state = renderOrgState()

    expect(state.notFound()).toBe(false)
    expect(state.orgId()).toBe('')
  })
})
