import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { errorText, successText } from '~/styles/shared.css'
import { StatusLine } from './StatusLine'

describe('statusLine', () => {
  it('renders nothing when there is no message', () => {
    const { container } = render(() => <StatusLine message={null} />)
    expect(container.textContent).toBe('')
  })

  // The colour is the whole reason this component exists: nine hand-written
  // ternaries each re-decided it, and a green "Failed to..." is what one that
  // drifts looks like.
  it('colours a success and an error differently', () => {
    const ok = render(() => <StatusLine message={{ type: 'success', text: 'Saved.' }} />)
    const bad = render(() => <StatusLine message={{ type: 'error', text: 'Nope.' }} />)
    const okClass = ok.getByText('Saved.').className
    const badClass = bad.getByText('Nope.').className
    expect(okClass).not.toBe(badClass)
    expect(okClass).toBe(successText)
    expect(badClass).toBe(errorText)
  })
})
