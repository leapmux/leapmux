/// <reference types="vitest/globals" />
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { BlockedReasonNotice } from './BlockedReasonNotice'

describe('blockedReasonNotice', () => {
  it('renders the reason under the shared testid', () => {
    render(() => <BlockedReasonNotice reason="Create a workspace first." />)
    expect(screen.getByTestId('new-tab-blocked-reason')).toHaveTextContent('Create a workspace first.')
  })

  it('renders nothing when there is no reason', () => {
    const { container } = render(() => <BlockedReasonNotice reason={undefined} />)
    expect(container.textContent).toBe('')
  })

  it('follows the reason reactively — an empty string hides the notice', () => {
    const [reason, setReason] = createSignal<string | undefined>('Not ready.')
    const { container } = render(() => <BlockedReasonNotice reason={reason()} />)
    expect(screen.getByTestId('new-tab-blocked-reason')).toBeInTheDocument()

    setReason(undefined)
    expect(container.textContent).toBe('')
  })

  it('re-appears when a reason arrives after mount', () => {
    const [reason, setReason] = createSignal<string | undefined>(undefined)
    render(() => <BlockedReasonNotice reason={reason()} />)
    expect(screen.queryByTestId('new-tab-blocked-reason')).toBeNull()

    setReason('The workspace view is not ready yet.')
    expect(screen.getByTestId('new-tab-blocked-reason')).toHaveTextContent('The workspace view is not ready yet.')
  })
})
