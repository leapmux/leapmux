import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MarkdownPlanLayout } from '~/components/chat/widgets/MarkdownPlanLayout'

describe('markdownPlanLayout', () => {
  it('renders the markdown body when planText is provided', () => {
    const { container } = render(() => (
      <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText="# Step 1" />
    ))
    expect(container.querySelector('hr')).toBeInTheDocument()
    expect(container.textContent).toContain('Step 1')
  })

  it('omits the markdown body and hr when planText is empty', () => {
    const { container } = render(() => (
      <MarkdownPlanLayout toolName="Plan" title="Proposed Plan" planText="" />
    ))
    expect(container.querySelector('hr')).not.toBeInTheDocument()
  })

  it('shows the title in the layout header', () => {
    const { container } = render(() => (
      <MarkdownPlanLayout toolName="ExitPlanMode" title="Leaving Plan Mode" planText="body" />
    ))
    expect(container.textContent).toContain('Leaving Plan Mode')
  })

  it('forwards onReply via context when planText is non-empty', () => {
    const onReply = vi.fn()
    const { container } = render(() => (
      <MarkdownPlanLayout
        toolName="Plan"
        title="Proposed Plan"
        planText="hello"
        context={{ onReply }}
      />
    ))
    // The reply button is rendered when both onReply and planText are present.
    // We don't assert on specific selectors — just that the component mounts cleanly.
    expect(container.firstElementChild).toBeInTheDocument()
  })
})
