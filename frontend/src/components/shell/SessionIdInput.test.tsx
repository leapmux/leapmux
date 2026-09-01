/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { RESUME_SESSION_ERROR_ID } from '~/components/shell/resumeSession'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSessionIdState } from '~/hooks/createSessionIdState'

// No plugin import here on purpose: `createSessionIdState` imports the provider
// barrel itself, and these cases are what proves it. A hook that depended on
// its caller to register the plugins would answer "token rule" for Pi and
// refuse every session path again.

const claude = () => AgentProvider.CLAUDE_CODE
const pi = () => AgentProvider.PI

// The shape Pi issues: its session directory is an escaped copy of the working
// directory, so a real path runs well past the 128-byte TOKEN cap. This one is
// 137 bytes -- the field refused it, and the worker demanded it.
const PI_SESSION_PATH
  = '/Users/dev/.pi/agent/sessions/--Users-dev-workspace-example-project--/'
    + '2026-08-28T03-13-15-703Z_018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b.jsonl'

describe('createSessionIdState', () => {
  it('trims whitespace into `trimmed()` and leaves `value()` raw', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(claude)
      state.setValue('  abc-123  ')
      expect(state.value()).toBe('  abc-123  ')
      expect(state.trimmed()).toBe('abc-123')
      dispose()
    })
  })

  it('reports null error for empty / whitespace-only values', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(claude)
      expect(state.error()).toBeNull()
      state.setValue('   ')
      expect(state.error()).toBeNull()
      dispose()
    })
  })

  it('surfaces validateSessionId errors for malformed input', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(claude)
      // validateSessionId rejects empty post-trim and overlong inputs;
      // a string with control characters is the simplest failure case.
      state.setValue('bad\x00id')
      expect(state.error()).not.toBeNull()
      dispose()
    })
  })

  it('clears the error when the input becomes valid again', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(claude)
      state.setValue('bad\x00id')
      expect(state.error()).not.toBeNull()
      state.setValue('good-id')
      expect(state.error()).toBeNull()
      dispose()
    })
  })

  it('keeps the token rule for a provider whose handle is a token', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(claude)
      state.setValue(PI_SESSION_PATH)
      expect(state.error()).toBe('Session ID must be at most 128 bytes')
      expect(state.isFilePath()).toBe(false)
      dispose()
    })
  })

  // The two halves of the launch failure this field caused. A Pi session path
  // is longer than the token cap, and a Pi session ID is not a path -- so with
  // one rule for both shapes, one of the two was always refused.
  it('accepts both shapes of a Pi resume handle', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(pi)
      expect(state.isFilePath()).toBe(true)
      state.setValue(PI_SESSION_PATH)
      expect(new TextEncoder().encode(PI_SESSION_PATH).length).toBeGreaterThan(128)
      expect(state.error()).toBeNull()
      state.setValue('018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b')
      expect(state.error()).toBeNull()
      state.setValue('~/.pi/agent/sessions/--p--/2026-08-28T03-13-15-703Z_018f4a2b.jsonl')
      expect(state.error()).toBeNull()
      dispose()
    })
  })

  it('refuses a Pi path that is relative, escaping or too long', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(pi)
      state.setValue('sessions/2026-08-28T03-13-15-703Z_018f4a2b.jsonl')
      expect(state.error()).toBe('Session file path must be absolute')
      state.setValue('session.jsonl')
      expect(state.error()).toBe('Session file path must be absolute')
      state.setValue('/var/pi/../../etc/passwd.jsonl')
      expect(state.error()).toBe('Session file path must not contain ".."')
      state.setValue(`/var/pi/${'a'.repeat(1024)}.jsonl`)
      expect(state.error()).toBe('Session file path must be at most 1024 bytes')
      dispose()
    })
  })

  // The ID half of a file-path provider's rule is the SAME token rule, so a
  // handle that argv could read as a flag is refused for Pi as well.
  it('applies the token rule to the ID half for a Pi handle', () => {
    createRoot((dispose) => {
      const state = createSessionIdState(pi)
      state.setValue('bad\x00id')
      expect(state.error()).not.toBeNull()
      state.setValue('-dangerous')
      expect(state.error()).toBe('Session ID must not start with a hyphen')
      dispose()
    })
  })

  // The dialog's provider selector and this field share one dialog, so the
  // rule has to follow a change of provider with the text already typed.
  it('re-runs the rule when the provider changes', () => {
    createRoot((dispose) => {
      const [provider, setProvider] = createSignal<AgentProvider | undefined>(AgentProvider.CLAUDE_CODE)
      const state = createSessionIdState(provider)
      state.setValue(PI_SESSION_PATH)
      expect(state.error()).toBe('Session ID must be at most 128 bytes')
      setProvider(AgentProvider.PI)
      expect(state.error()).toBeNull()
      dispose()
    })
  })
})

describe('sessionIdInput', () => {
  it('renders the value and forwards onInput to setValue', () => {
    const state = createSessionIdState(claude)
    render(() => <SessionIdInput state={state} />)
    const input = screen.getByPlaceholderText('Session ID') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'session-1' } })
    expect(state.value()).toBe('session-1')
    expect(state.trimmed()).toBe('session-1')
  })

  it('names both shapes in the placeholder for a file-path provider', () => {
    const state = createSessionIdState(pi)
    render(() => <SessionIdInput state={state} />)
    expect(screen.getByPlaceholderText('Session ID or file path')).toBeInTheDocument()
  })
})

describe('sessionIdInput accessibility', () => {
  // The visible label is a plain `div`, so without an explicit aria-label the
  // PLACEHOLDER becomes the last-resort accessible name — and that name then
  // changes with the selected provider, from "Session ID" to "Session ID or
  // file path". TitleInput answers this the same way, in this same dialog.
  it('gives the input a stable accessible name, not the placeholder', () => {
    const claudeState = createSessionIdState(claude)
    render(() => <SessionIdInput state={claudeState} />)
    expect(screen.getByLabelText('Resume an existing session')).toBeInTheDocument()

    const piState = createSessionIdState(pi)
    render(() => <SessionIdInput state={piState} />)
    // Both inputs answer to the same name although their placeholders differ.
    expect(screen.getAllByLabelText('Resume an existing session')).toHaveLength(2)
    expect(screen.getByPlaceholderText('Session ID or file path')).toBeInTheDocument()
  })

  // The input names the error node; `ResumeSessionField` renders it. The two
  // halves meeting is asserted in that field's own test, where both are mounted.
  it('points at the field error node while the value is refused', () => {
    const state = createSessionIdState(claude)
    render(() => <SessionIdInput state={state} />)
    const input = screen.getByLabelText('Resume an existing session')
    fireEvent.input(input, { target: { value: '--dangerous' } })

    expect(state.error()).not.toBeNull()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAttribute('aria-describedby', RESUME_SESSION_ERROR_ID)
  })

  it('marks the input valid while the handle is acceptable', () => {
    const state = createSessionIdState(claude)
    render(() => <SessionIdInput state={state} />)
    const input = screen.getByLabelText('Resume an existing session')

    expect(input).not.toHaveAttribute('aria-invalid')
    expect(input).not.toHaveAttribute('aria-describedby')
  })
})
