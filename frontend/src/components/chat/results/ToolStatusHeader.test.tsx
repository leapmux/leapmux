import { render } from '@solidjs/testing-library'
import CircleAlert from 'lucide-solid/icons/circle-alert'
import { describe, expect, it } from 'vitest'
import { drawsOwnOutcome, ToolOutcomeHeader } from './ToolStatusHeader'

describe('tooloutcomeheader', () => {
  it('states the outcome when no enclosing renderer does', () => {
    const { container } = render(() => <ToolOutcomeHeader when icon={CircleAlert} title="Failed" />)
    expect(container.textContent).toContain('Failed')
  })

  // The row above draws the retained completion, so a header here would state the same
  // outcome twice. Every renderer routes through this component for that reason.
  it('draws nothing when the enclosing renderer states the outcome', () => {
    const { container } = render(() => (
      <ToolOutcomeHeader when icon={CircleAlert} title="Failed" context={{ completionHeader: true }} />
    ))
    expect(container.textContent).toBe('')
  })

  it('draws nothing when the renderer has no reason of its own', () => {
    const { container } = render(() => <ToolOutcomeHeader when={false} icon={CircleAlert} title="Failed" />)
    expect(container.textContent).toBe('')
  })
})

describe('drawsownoutcome', () => {
  it('answers for an absent context and for an unset flag', () => {
    expect(drawsOwnOutcome(undefined)).toBe(true)
    expect(drawsOwnOutcome({})).toBe(true)
    expect(drawsOwnOutcome({ completionHeader: false })).toBe(true)
    expect(drawsOwnOutcome({ completionHeader: true })).toBe(false)
  })
})
