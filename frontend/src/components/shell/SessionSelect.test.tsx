import type { AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NEW_SESSION_LABEL, sessionOptionLabel, SessionSelect } from '~/components/shell/SessionSelect'
import { menuOptions, menuOptionValues, menuTriggerText, pickMenuValue } from '~/test-support/menu'

afterEach(() => {
  cleanup()
})

const MENU = 'session-select-menu'
const NOW = new Date('2026-09-01T12:00:00.000Z')

function summary(overrides: Partial<AgentSessionSummary> = {}): AgentSessionSummary {
  return {
    $typeName: 'leapmux.v1.AgentSessionSummary',
    sessionId: 'ses_1',
    title: 'Build the thing',
    updatedAt: '2026-09-01T11:00:00.000Z',
    ...overrides,
  } as AgentSessionSummary
}

function renderSelect(overrides: Partial<Parameters<typeof SessionSelect>[0]> = {}) {
  const onChange = vi.fn()
  render(() => (
    <SessionSelect
      value=""
      onChange={onChange}
      sessions={[summary()]}
      loading={false}
      {...overrides}
    />
  ))
  return { onChange }
}

describe('sessionOptionLabel', () => {
  it('states the title and how long ago the session ran', () => {
    expect(sessionOptionLabel(summary(), NOW)).toBe('Build the thing — 1h ago')
  })

  // Pi stores no title and a Claude session has none until its title pass
  // runs, so the handle stands in -- a string the user can recognise, unlike a
  // placeholder that would read the same on every such row.
  it('falls back to the handle when the store recorded no title', () => {
    expect(sessionOptionLabel(summary({ title: '', sessionId: 'ses_abc' }), NOW)).toBe('ses_abc — 1h ago')
    expect(sessionOptionLabel(summary({ title: '   ', sessionId: 'ses_abc' }), NOW)).toBe('ses_abc — 1h ago')
  })

  it('omits the age when the store recorded no time', () => {
    expect(sessionOptionLabel(summary({ updatedAt: '' }), NOW)).toBe('Build the thing')
    expect(sessionOptionLabel(summary({ updatedAt: 'not a timestamp' }), NOW)).toBe('Build the thing')
  })
})

describe('sessionSelect', () => {
  it('offers one row per session, newest order preserved', () => {
    renderSelect({
      sessions: [
        summary({ sessionId: 'ses_a', title: 'Newest' }),
        summary({ sessionId: 'ses_b', title: 'Older', updatedAt: '2026-08-30T12:00:00.000Z' }),
      ],
    })
    expect(menuOptionValues(MENU)).toEqual(['', 'ses_a', 'ses_b'])
  })

  // A real, selectable row and not a prompt: it is how the user takes a pick
  // back after choosing a session.
  it('leads with a row that starts a new session instead', () => {
    const { onChange } = renderSelect({ value: 'ses_1' })
    expect(menuOptions(MENU)[0]).toBe(NEW_SESSION_LABEL)

    pickMenuValue(MENU, '')
    expect(onChange).toHaveBeenCalledWith('')
  })

  it('passes the raw handle to onChange, not the label', () => {
    const { onChange } = renderSelect({ sessions: [summary({ sessionId: 'ses_xyz', title: 'Some work' })] })
    pickMenuValue(MENU, 'ses_xyz')
    expect(onChange).toHaveBeenCalledWith('ses_xyz')
  })

  it('shows the chosen session on the trigger', () => {
    renderSelect({ value: 'ses_1' })
    expect(menuTriggerText(MENU)).toContain('Build the thing')
  })

  it('prompts to start a new session while nothing is chosen', () => {
    renderSelect({ value: '' })
    expect(menuTriggerText(MENU)).toContain(NEW_SESSION_LABEL)
  })

  it('shows the loading label while the list is on its way', () => {
    renderSelect({ loading: true, sessions: [] })
    expect(menuTriggerText(MENU)).toContain('Loading sessions...')
  })

  // The filter box is passed explicitly, so it is present at ANY list length --
  // LoadingMenu would otherwise only derive one past a dozen entries, and
  // finding one session by eye is the work this field exists to remove.
  it('offers a filter box even for a short list', () => {
    renderSelect({
      sessions: [
        summary({ sessionId: 'ses_a', title: 'Refactor the parser' }),
        summary({ sessionId: 'ses_b', title: 'Fix the flaky test' }),
      ],
    })
    const filter = screen.getByTestId('loading-menu-filter')
    expect(filter).toBeInTheDocument()

    fireEvent.input(filter, { target: { value: 'flaky' } })
    expect(menuOptionValues(MENU)).toEqual(['ses_b'])
  })

  it('filters on the age as well as the title, because both are in the label', () => {
    renderSelect({
      sessions: [
        summary({ sessionId: 'ses_recent', title: 'Recent', updatedAt: new Date().toISOString() }),
        summary({ sessionId: 'ses_ancient', title: 'Ancient', updatedAt: '2020-01-01T00:00:00.000Z' }),
      ],
    })
    fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'y ago' } })
    expect(menuOptionValues(MENU)).toEqual(['ses_ancient'])
  })

  it('names itself for a screen reader with the field label', () => {
    renderSelect()
    expect(screen.getAllByLabelText('Resume an existing session').length).toBeGreaterThan(0)
  })
})
