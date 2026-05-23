/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { createSessionIdState } from '~/hooks/createSessionIdState'

describe('createSessionIdState', () => {
  it('trims whitespace into `trimmed()` and leaves `value()` raw', () => {
    createRoot((dispose) => {
      const state = createSessionIdState()
      state.setValue('  abc-123  ')
      expect(state.value()).toBe('  abc-123  ')
      expect(state.trimmed()).toBe('abc-123')
      dispose()
    })
  })

  it('reports null error for empty / whitespace-only values', () => {
    createRoot((dispose) => {
      const state = createSessionIdState()
      expect(state.error()).toBeNull()
      state.setValue('   ')
      expect(state.error()).toBeNull()
      dispose()
    })
  })

  it('surfaces validateSessionId errors for malformed input', () => {
    createRoot((dispose) => {
      const state = createSessionIdState()
      // validateSessionId rejects empty post-trim and overlong inputs;
      // a string with control characters is the simplest failure case.
      state.setValue('bad\x00id')
      expect(state.error()).not.toBeNull()
      dispose()
    })
  })

  it('clears the error when the input becomes valid again', () => {
    createRoot((dispose) => {
      const state = createSessionIdState()
      state.setValue('bad\x00id')
      expect(state.error()).not.toBeNull()
      state.setValue('good-id')
      expect(state.error()).toBeNull()
      dispose()
    })
  })
})

describe('sessionIdInput', () => {
  it('renders the value and forwards onInput to setValue', () => {
    const state = createSessionIdState()
    render(() => <SessionIdInput state={state} />)
    const input = screen.getByPlaceholderText('Session ID') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'session-1' } })
    expect(state.value()).toBe('session-1')
    expect(state.trimmed()).toBe('session-1')
  })

  it('shows the error row when state.error() is non-null', () => {
    const state = createSessionIdState()
    state.setValue('bad\x00id')
    render(() => <SessionIdInput state={state} />)
    expect(state.error()).not.toBeNull()
    expect(screen.getByText(state.error()!)).toBeInTheDocument()
  })

  it('hides the error row for a valid value', () => {
    const state = createSessionIdState()
    state.setValue('valid-id')
    render(() => <SessionIdInput state={state} />)
    expect(screen.queryByText(/Invalid/i)).toBeNull()
  })
})
