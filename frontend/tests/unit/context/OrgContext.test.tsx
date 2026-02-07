import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { OrgProvider, useOrg } from '~/context/OrgContext'

// Mock the orgClient
const mockListMyOrgs = vi.fn()
vi.mock('~/api/clients', () => ({
  orgClient: {
    listMyOrgs: (...args: unknown[]) => mockListMyOrgs(...args),
  },
}))

// Mock useParams to return a controlled orgSlug
let mockOrgSlug = 'test-org'
vi.mock('@solidjs/router', () => ({
  useParams: () => ({ orgSlug: mockOrgSlug }),
}))

// Test helper component that captures org state
function OrgStateCapture(props: { onState: (state: ReturnType<typeof useOrg>) => void }) {
  const org = useOrg()
  props.onState(org)
  return <div data-testid="capture">captured</div>
}

describe('orgContext', () => {
  it('notFound is true when slug does not match any org', async () => {
    mockOrgSlug = 'nonexistent-org'
    mockListMyOrgs.mockResolvedValue({
      orgs: [
        { id: 'org-1', name: 'my-org' },
      ],
    })

    let capturedState: ReturnType<typeof useOrg> | null = null

    render(() => (
      <OrgProvider>
        <OrgStateCapture onState={(s) => { capturedState = s }} />
      </OrgProvider>
    ))

    await waitFor(() => {
      expect(capturedState).not.toBeNull()
      expect(capturedState!.loading()).toBe(false)
    })

    expect(capturedState!.notFound()).toBe(true)
    expect(capturedState!.orgId()).toBe('')
  })

  it('notFound is false when slug matches an org', async () => {
    mockOrgSlug = 'my-org'
    mockListMyOrgs.mockResolvedValue({
      orgs: [
        { id: 'org-1', name: 'my-org' },
      ],
    })

    let capturedState: ReturnType<typeof useOrg> | null = null

    render(() => (
      <OrgProvider>
        <OrgStateCapture onState={(s) => { capturedState = s }} />
      </OrgProvider>
    ))

    await waitFor(() => {
      expect(capturedState).not.toBeNull()
      expect(capturedState!.loading()).toBe(false)
    })

    expect(capturedState!.notFound()).toBe(false)
    expect(capturedState!.orgId()).toBe('org-1')
  })

  it('notFound is false during loading', async () => {
    mockOrgSlug = 'any-org'
    // Never resolve to keep loading state
    mockListMyOrgs.mockReturnValue(new Promise(() => {}))

    let capturedState: ReturnType<typeof useOrg> | null = null

    render(() => (
      <OrgProvider>
        <OrgStateCapture onState={(s) => { capturedState = s }} />
      </OrgProvider>
    ))

    await waitFor(() => {
      expect(capturedState).not.toBeNull()
    })

    // While loading, notFound should be false
    expect(capturedState!.loading()).toBe(true)
    expect(capturedState!.notFound()).toBe(false)
  })

  it('notFound is false when fetch errors', async () => {
    mockOrgSlug = 'any-org'
    mockListMyOrgs.mockRejectedValue(new Error('network error'))

    let capturedState: ReturnType<typeof useOrg> | null = null

    render(() => (
      <OrgProvider>
        <OrgStateCapture onState={(s) => { capturedState = s }} />
      </OrgProvider>
    ))

    await waitFor(() => {
      expect(capturedState).not.toBeNull()
      expect(capturedState!.loading()).toBe(false)
    })

    // On error, notFound should be false (we don't know if the org exists)
    expect(capturedState!.notFound()).toBe(false)
  })
})
