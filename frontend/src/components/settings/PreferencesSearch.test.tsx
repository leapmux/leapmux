import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PreferencesSearch } from './PreferencesSearch'

afterEach(() => {
  cleanup()
})

describe('preferencesSearch', () => {
  it('does not steal focus when / is typed in another input', () => {
    render(() => (
      <div>
        <input aria-label="Other" />
        <PreferencesSearch query="" onQuery={vi.fn()} />
      </div>
    ))
    const other = screen.getByLabelText('Other') as HTMLInputElement
    other.focus()
    fireEvent.keyDown(document, { key: '/' })
    expect(document.activeElement).toBe(other)
    expect(document.activeElement).not.toBe(screen.getByTestId('preferences-search'))
  })

  it('escape with an empty query does not preventDefault', () => {
    const onQuery = vi.fn()
    render(() => <PreferencesSearch query="" onQuery={onQuery} />)
    const search = screen.getByTestId('preferences-search')
    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    search.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
    expect(onQuery).not.toHaveBeenCalled()
  })
})
