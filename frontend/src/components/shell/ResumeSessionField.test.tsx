import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { ResumeSessionField } from '~/components/shell/ResumeSessionField'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSessionIdState } from '~/hooks/createSessionIdState'
import { menuOptionValues, menuTrigger, menuTriggerText, pickMenuValue } from '~/test-support/menu'

vi.mock('~/api/workerRpc', () => ({ listAgentSessions: vi.fn() }))

const listAgentSessions = vi.mocked(workerRpc.listAgentSessions)

const MENU = 'session-select-menu'
const LABEL = 'Resume an existing session'

function response(...ids: string[]) {
  return {
    $typeName: 'leapmux.v1.ListAgentSessionsResponse',
    sessions: ids.map(sessionId => ({
      $typeName: 'leapmux.v1.AgentSessionSummary',
      sessionId,
      title: `Title for ${sessionId}`,
      updatedAt: '2026-09-01T11:00:00.000Z',
    })),
  } as never
}

const flush = () => new Promise(resolve => setTimeout(resolve, 0))

interface FieldOverrides {
  workerId?: string
  workingDir?: string
  agentProvider?: AgentProvider
}

// Each prop is passed explicitly rather than spread. Solid's `mergeProps`
// SKIPS an undefined value so a default shows through, so a spread could never
// express "the dialog has no provider selected yet" -- which is one of the
// three cases that must not fire the RPC.
function renderField(overrides: FieldOverrides = {}) {
  const state = createSessionIdState(() => AgentProvider.CLAUDE_CODE)
  const provider = 'agentProvider' in overrides ? overrides.agentProvider : AgentProvider.CLAUDE_CODE
  render(() => (
    <ResumeSessionField
      state={state}
      workerId={overrides.workerId ?? 'w-1'}
      workingDir={overrides.workingDir ?? '/repo'}
      agentProvider={provider}
    />
  ))
  return { state }
}

/** The fallback control: the text input the field shows with no list. */
const textInput = () => screen.queryByPlaceholderText(/^Session ID/)

const refreshButton = () => screen.getByTestId('session-field-refresh')

beforeEach(() => {
  listAgentSessions.mockReset()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('resumeSessionField', () => {
  it('shows the menu once the worker offers sessions', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a', 'ses_b'))
    renderField()
    await flush()

    expect(menuTrigger(MENU)).toBeInTheDocument()
    expect(textInput()).toBeNull()
  })

  // The capability the fallback preserves: a directory with no history would
  // otherwise leave a disabled menu and no way to resume at all.
  it('falls back to the text input when the worker offers nothing', async () => {
    listAgentSessions.mockResolvedValue(response())
    renderField()
    await flush()

    expect(textInput()).toBeInTheDocument()
    expect(screen.queryByTestId(`${MENU}-trigger`)).toBeNull()
  })

  it('falls back to the text input when the worker cannot answer', async () => {
    listAgentSessions.mockRejectedValue(new Error('worker offline'))
    renderField()
    await flush()

    expect(textInput()).toBeInTheDocument()
  })

  // The control must not swap under the user between the dialog opening and
  // the answer arriving.
  it('holds the menu while the list is on its way', async () => {
    listAgentSessions.mockReturnValue(new Promise(() => {}))
    renderField()
    await flush()

    expect(menuTriggerText(MENU)).toContain('Loading sessions...')
    expect(textInput()).toBeNull()
  })

  it('does not ask the worker until it knows all three keys', async () => {
    renderField({ workingDir: '' })
    await flush()
    expect(listAgentSessions).not.toHaveBeenCalled()

    cleanup()
    renderField({ agentProvider: undefined })
    await flush()
    expect(listAgentSessions).not.toHaveBeenCalled()

    cleanup()
    renderField({ workerId: '' })
    await flush()
    expect(listAgentSessions).not.toHaveBeenCalled()
  })

  it('writes the picked handle into the shared session state', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state } = renderField()
    await flush()

    pickMenuValue(MENU, 'ses_a')
    expect(state.trimmed()).toBe('ses_a')
    // A handle the worker issued must pass the field's own validation, or the
    // dialog's submit gate would refuse a session the user picked from a list.
    expect(state.error()).toBeNull()
  })

  it('clears the selection through the new-session row', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state } = renderField()
    await flush()

    pickMenuValue(MENU, 'ses_a')
    expect(state.trimmed()).toBe('ses_a')
    pickMenuValue(MENU, '')
    expect(state.trimmed()).toBe('')
  })

  // A value SURVIVES the swap, so an invalid one typed while the list was
  // empty would otherwise leave Create disabled above a menu that states
  // nothing about why.
  it('states the validation error above the menu too', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state } = renderField()
    await flush()

    state.setValue('--dangerously-skip-permissions')
    expect(state.error()).not.toBeNull()
    expect(screen.getByText(state.error()!)).toBeInTheDocument()
    expect(screen.getByText(state.error()!)).toHaveAttribute('role', 'alert')
  })

  // The field renders the error node and the input names it. Neither half is
  // provable alone, so the pair is asserted here, where both are mounted.
  it('resolves the fallback input aria-describedby to the live error node', async () => {
    listAgentSessions.mockResolvedValue(response())
    const { state } = renderField()
    await flush()

    fireEvent.input(textInput()!, { target: { value: '--dangerously-skip-permissions' } })
    const describedBy = textInput()!.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const message = document.getElementById(describedBy!)
    expect(message).toHaveTextContent(state.error()!)
    expect(message).toHaveAttribute('role', 'alert')
  })

  // The whole point of the button: a failure mounts the TEXT INPUT, so a
  // refresh that lived with the menu would be absent in the one state that
  // needs it. The hook's own effect re-fires only on a change of worker,
  // directory or provider, and none of the three changed here.
  it('recovers from a failed fetch when the user presses refresh', async () => {
    listAgentSessions.mockRejectedValueOnce(new Error('worker offline'))
    renderField()
    await flush()
    expect(textInput()).toBeInTheDocument()

    listAgentSessions.mockResolvedValue(response('ses_a'))
    fireEvent.click(refreshButton())
    await flush()

    expect(menuTrigger(MENU)).toBeInTheDocument()
    expect(textInput()).toBeNull()
    expect(listAgentSessions).toHaveBeenCalledTimes(2)
  })

  // The button belongs to the FIELD, not to either control, so a list that
  // went stale while the dialog sat open is refreshable without first emptying
  // it. A user who closed a tab to free its session needs exactly this.
  it('refreshes a list that is already showing', async () => {
    listAgentSessions.mockResolvedValueOnce(response('ses_a'))
    renderField()
    await flush()
    expect(menuOptionValues(MENU)).toEqual(['', 'ses_a'])

    listAgentSessions.mockResolvedValue(response('ses_a', 'ses_b'))
    fireEvent.click(refreshButton())
    await flush()

    expect(menuOptionValues(MENU)).toEqual(['', 'ses_a', 'ses_b'])
  })

  it('refuses a second fetch while one is in flight', async () => {
    listAgentSessions.mockReturnValue(new Promise(() => {}))
    renderField()
    await flush()

    expect(refreshButton()).toBeDisabled()
    fireEvent.click(refreshButton())
    await flush()
    expect(listAgentSessions).toHaveBeenCalledTimes(1)
  })

  // Pressing it would ask for the sessions of nowhere, so it states that it
  // cannot act instead of looking live and doing nothing.
  it('disables refresh until it knows all three keys', async () => {
    renderField({ workingDir: '' })
    await flush()
    expect(refreshButton()).toBeDisabled()
  })

  // Both controls answer to ONE accessible name, so a screen-reader user is
  // not told the field became a different field when the list arrives.
  it('keeps one accessible name across the swap', async () => {
    listAgentSessions.mockResolvedValue(response())
    renderField()
    await flush()
    expect(screen.getByLabelText(LABEL)).toBeInTheDocument()

    cleanup()
    listAgentSessions.mockResolvedValue(response('ses_a'))
    renderField()
    await flush()
    expect(screen.getAllByLabelText(LABEL).length).toBeGreaterThan(0)
  })
})
