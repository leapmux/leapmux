import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { RESUME_SESSION_ERROR_ID } from '~/components/shell/resumeSession'
import { ResumeSessionField } from '~/components/shell/ResumeSessionField'
import { TYPE_A_HANDLE_VALUE } from '~/components/shell/SessionSelect'
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

/** The way back to the menu, which only the text-box state offers. */
const pickFromListButton = () => screen.queryByTestId('session-field-pick-from-list')

// A harness whose working directory can CHANGE, which the fixed-prop one above
// cannot express. The directory is the field's most-used key: the user picks it
// from a tree while the dialog stays open.
function renderFieldWithMovableDir(initialDir = '/repo') {
  const state = createSessionIdState(() => AgentProvider.CLAUDE_CODE)
  const [dir, setDir] = createSignal(initialDir)
  render(() => (
    <ResumeSessionField
      state={state}
      workerId="w-1"
      workingDir={dir()}
      agentProvider={AgentProvider.CLAUDE_CODE}
    />
  ))
  return { state, setDir }
}

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
    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE, 'ses_a'])

    listAgentSessions.mockResolvedValue(response('ses_a', 'ses_b'))
    fireEvent.click(refreshButton())
    await flush()

    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE, 'ses_a', 'ses_b'])
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

  // A handle belongs to ONE directory. Carried across a change of directory it
  // is not merely stale: the menu finds no matching option and shows the raw
  // handle as though it were selected, Create stays enabled because the handle
  // is syntactically valid, and the worker is asked to resume another
  // directory's conversation under this one.
  it('drops a picked handle when the directory changes', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state, setDir } = renderFieldWithMovableDir()
    await flush()

    pickMenuValue(MENU, 'ses_a')
    expect(state.trimmed()).toBe('ses_a')

    listAgentSessions.mockResolvedValue(response('ses_b'))
    setDir('/other')
    await flush()

    expect(state.trimmed()).toBe('')
    expect(menuTriggerText(MENU)).toBe('Start a new session')
  })

  // The same rule for a handle the user TYPED: the text box is the fallback for
  // a directory with no sessions, and its value belongs to that directory too.
  it('drops a typed handle when the directory changes', async () => {
    listAgentSessions.mockResolvedValue(response())
    const { state, setDir } = renderFieldWithMovableDir()
    await flush()

    const input = textInput()
    expect(input).toBeInTheDocument()
    fireEvent.input(input!, { target: { value: 'ses_typed' } })
    expect(state.trimmed()).toBe('ses_typed')

    setDir('/other')
    await flush()
    expect(state.trimmed()).toBe('')
  })

  // The first paint has no answer yet, and an empty list is not the same
  // statement as an unasked question. Showing the text box first and replacing
  // it once the effect ran took the control out from under a user mid-keystroke.
  it('shows the menu, not the text box, before the first answer arrives', async () => {
    listAgentSessions.mockReturnValue(new Promise(() => {}))
    renderField()

    expect(textInput()).not.toBeInTheDocument()
    expect(menuTrigger(MENU)).toBeInTheDocument()
  })

  // Refreshing must not swap the control either: a failed fetch is exactly the
  // case that mounts the text box, and the button beside it is the way back.
  it('keeps the text box mounted while a refresh is in flight', async () => {
    listAgentSessions.mockResolvedValueOnce(response())
    renderField()
    await flush()
    expect(textInput()).toBeInTheDocument()

    listAgentSessions.mockReturnValue(new Promise(() => {}))
    fireEvent.click(refreshButton())
    expect(textInput()).toBeInTheDocument()
  })

  // The list holds only what this worker can enumerate. A handle from another
  // machine, or one a tab already holds open, is absent from a list that is not
  // empty -- so the menu has to offer a way back to typing.
  it('swaps to the text box when the user asks to type a handle', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state } = renderFieldWithMovableDir()
    await flush()
    expect(textInput()).not.toBeInTheDocument()

    pickMenuValue(MENU, TYPE_A_HANDLE_VALUE)

    expect(textInput()).toBeInTheDocument()
    // The sentinel is not a handle, so it must never reach the state.
    expect(state.trimmed()).toBe('')
  })

  // The route INTO the text box is a menu row, so the route out cannot be one.
  // Without this button, a user who picked "Enter a session ID…" by mistake was
  // held in the text box for the life of the dialog: nothing else clears that
  // state, and the field re-keys only on a change of worker, directory or
  // provider.
  it('returns to the menu from the text box, dropping what was typed', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    const { state } = renderField()
    await flush()

    pickMenuValue(MENU, TYPE_A_HANDLE_VALUE)
    fireEvent.input(textInput()!, { target: { value: 'ses_from_elsewhere' } })
    expect(state.trimmed()).toBe('ses_from_elsewhere')

    fireEvent.click(pickFromListButton()!)

    expect(menuTrigger(MENU)).toBeInTheDocument()
    expect(textInput()).toBeNull()
    // Cleared, exactly as the row that switched TO the text box clears a picked
    // handle: a typed handle is not one the menu can offer, so carrying it back
    // would leave the trigger showing raw text the list does not hold.
    expect(state.trimmed()).toBe('')
    expect(menuTriggerText(MENU)).toBe('Start a new session')
  })

  // The button states a way back that exists. With no list to return to,
  // pressing it would put the same text box straight back.
  it('offers no way back while the text box is the only control', async () => {
    listAgentSessions.mockResolvedValue(response())
    renderField()
    await flush()

    expect(textInput()).toBeInTheDocument()
    expect(pickFromListButton()).toBeNull()
  })

  it('offers no way back while the menu is already showing', async () => {
    listAgentSessions.mockResolvedValue(response('ses_a'))
    renderField()
    await flush()

    expect(pickFromListButton()).toBeNull()
  })

  // The error travels with the FIELD, so it survives the swap -- and whichever
  // control is mounted has to point at it. The menu carried neither attribute,
  // so a screen-reader user who returned to the trigger was told nothing was
  // wrong beside a Create button that refused to run.
  //
  // The reachable path is a REFRESH, not a directory change: the directory
  // clears the handle with it, while a refresh leaves the typed value in place
  // and can still turn an empty list into a populated one.
  it('points the menu trigger at the field error that survived the swap', async () => {
    listAgentSessions.mockResolvedValueOnce(response())
    const { state } = renderField()
    await flush()

    // A control character is the simplest handle the shared token rule refuses.
    fireEvent.input(textInput()!, { target: { value: 'bad\x00id' } })
    expect(state.error()).not.toBeNull()

    listAgentSessions.mockResolvedValue(response('ses_a'))
    fireEvent.click(refreshButton())
    await flush()

    const trigger = menuTrigger(MENU)
    expect(trigger).toBeInTheDocument()
    expect(trigger.getAttribute('aria-invalid')).toBe('true')
    const describedBy = trigger.getAttribute('aria-describedby')
    expect(describedBy).toBe(RESUME_SESSION_ERROR_ID)
    // The id must resolve to the live message, not merely be present.
    expect(document.getElementById(describedBy!)?.textContent).toBe(state.error())
  })
})
