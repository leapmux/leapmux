import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { Alert } from './Alert'

describe('alert', () => {
  it('renders a role=alert with a bold label and body', () => {
    const { container } = render(() => <Alert label="System Reminder">hello world</Alert>)
    const el = container.querySelector('[role="alert"]')
    expect(el).not.toBeNull()
    expect(el!.querySelector('strong')?.textContent).toBe('System Reminder')
    expect(el!.textContent).toContain('hello world')
  })

  it('omits data-variant by default and sets it when provided', () => {
    const { container: info } = render(() => <Alert>info body</Alert>)
    expect(info.querySelector('[role="alert"]')!.hasAttribute('data-variant')).toBe(false)

    const { container: err } = render(() => <Alert variant="error">oops</Alert>)
    expect(err.querySelector('[role="alert"]')!.getAttribute('data-variant')).toBe('error')
  })

  it('omits the label entirely when none is given', () => {
    const { container } = render(() => <Alert>just a body</Alert>)
    expect(container.querySelector('strong')).toBeNull()
  })

  it('escapes HTML in the body so markup cannot be injected', () => {
    const { container } = render(() => <Alert>{'<script>alert(1)</script>'}</Alert>)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('[role="alert"]')!.textContent).toContain('<script>alert(1)</script>')
  })
})
