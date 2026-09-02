import type { AgentSessionSummary } from '~/generated/proto/leapmux/v1/agent_pb'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RESUME_SESSION_ERROR_ID, typeAHandleLabel } from '~/components/shell/resumeSession'
import {
  NEW_SESSION_LABEL,
  sessionOptionDetail,
  sessionOptionLabel,
  SessionSelect,
  TYPE_A_HANDLE_VALUE,
} from '~/components/shell/SessionSelect'
import { clippedText } from '~/styles/shared.css'
import { hoverForTooltip, stubClipped } from '~/test-support/clipStub'
import {
  menuOptionLabels,
  menuOptions,
  menuOptionValues,
  menuTriggerText,
  openMenu,
  pickMenuValue,
} from '~/test-support/menu'

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
      invalid={false}
      isFilePath={false}
      {...overrides}
    />
  ))
  return { onChange }
}

describe('sessionOptionLabel', () => {
  it('states the title', () => {
    expect(sessionOptionLabel(summary())).toBe('Build the thing')
  })

  // Pi stores no title and a Claude session has none until its title pass
  // runs, so the handle stands in -- a string the user can recognise, unlike a
  // placeholder that would read the same on every such row.
  it('falls back to the handle when the store recorded no title', () => {
    expect(sessionOptionLabel(summary({ title: '', sessionId: 'ses_abc' }))).toBe('ses_abc')
    expect(sessionOptionLabel(summary({ title: '   ', sessionId: 'ses_abc' }))).toBe('ses_abc')
  })

  // The age is a column of its own, so a title long enough to clip does not
  // take the timestamp with it.
  it('leaves the age out, whatever the store recorded', () => {
    expect(sessionOptionLabel(summary({ updatedAt: '' }))).toBe('Build the thing')
    expect(sessionOptionLabel(summary())).toBe('Build the thing')
  })
})

describe('sessionOptionDetail', () => {
  it('states how long ago the session ran, as text the filter can match', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(NOW)
      expect(sessionOptionDetail(summary())?.text()).toBe('1h ago')
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The whole point of the accessor: it is read at FILTER time, so it states the
  // age the row is SHOWING rather than the one it showed when the option list
  // was built. Frozen, it drifted away from the live `RelativeTime` beside it,
  // and typing the age on screen emptied the list.
  it('re-reads the clock on every call, so it cannot drift from the drawn age', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(NOW)
      const detail = sessionOptionDetail(summary())!
      expect(detail.text()).toBe('1h ago')

      vi.setSystemTime(new Date(NOW.getTime() + 3 * 60 * 60 * 1000))
      expect(detail.text()).toBe('4h ago')
    }
    finally {
      vi.useRealTimers()
    }
  })

  // No time, no column: an empty detail would draw a gap at the right end of
  // the row and give the filter an empty string to match everything with.
  it('gives no detail when the store recorded no time', () => {
    expect(sessionOptionDetail(summary({ updatedAt: '' }))).toBeUndefined()
    expect(sessionOptionDetail(summary({ updatedAt: 'not a timestamp' }))).toBeUndefined()
  })

  // The rendered form is `RelativeTime`, not the plain text: that is what puts
  // the full local date and time one hover away from a row that has room for
  // "4h ago" and nothing more.
  it('renders the age as a timestamp whose hover states the full date', () => {
    vi.useFakeTimers()
    try {
      const detail = sessionOptionDetail(summary())
      const { container } = render(() => <>{detail!.render!()}</>)

      // Wrapper -> the span Tooltip resolves as its target.
      const target = container.firstElementChild!.firstElementChild as HTMLElement
      expect(target.textContent).toContain('ago')
      expect(hoverForTooltip(target)?.textContent).toContain('2026')
    }
    finally {
      vi.useRealTimers()
    }
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
    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE, 'ses_a', 'ses_b'])
  })

  // The list holds only what this worker can enumerate, so a handle from
  // another machine, one a tab already holds open, and one past the worker's
  // cap are all missing from it. Without this row a directory with a single
  // session would take typing away entirely.
  it('offers a row that hands the field back to its text box, above the sessions', () => {
    const { onChange } = renderSelect({ sessions: [summary({ sessionId: 'ses_a' })] })
    // Second, beside the other row that is not a session: at the bottom it sat
    // under a list that runs to the worker's cap, so the answer to "my session
    // is not here" was the one row a user had to scroll to find.
    expect(menuOptions(MENU)[1]).toBe(typeAHandleLabel(false))

    pickMenuValue(MENU, TYPE_A_HANDLE_VALUE)
    expect(onChange).toHaveBeenCalledWith(TYPE_A_HANDLE_VALUE)
  })

  // The row is the one sentence a user reads BEFORE the text box exists, so it
  // has to invite the same shapes the box's placeholder does. Pi's session is a
  // file, and a row that said "Enter a session ID…" invited the shape the
  // worker then refused with "path must be absolute".
  it('invites a file path too when the provider takes one', () => {
    renderSelect({ isFilePath: true, sessions: [summary({ sessionId: 'ses_a' })] })
    expect(menuOptions(MENU)[1]).toBe('Enter a session ID or a file path…')
  })

  it('marks the trigger invalid and points it at the field error', () => {
    renderSelect({ invalid: true })
    const trigger = screen.getByTestId(`${MENU}-trigger`)
    expect(trigger.getAttribute('aria-invalid')).toBe('true')
    expect(trigger.getAttribute('aria-describedby')).toBe(RESUME_SESSION_ERROR_ID)
  })

  // The normal state: a handle the menu offered always validates, so the
  // trigger must not announce an error that is not there.
  it('leaves the trigger unmarked when the value is acceptable', () => {
    renderSelect({ invalid: false })
    const trigger = screen.getByTestId(`${MENU}-trigger`)
    expect(trigger.getAttribute('aria-invalid')).toBeNull()
    expect(trigger.getAttribute('aria-describedby')).toBeNull()
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

    // The two rows that are not sessions survive: they are the ways OUT of the
    // list, and a query is exactly when a user reaches for one. See below.
    fireEvent.input(filter, { target: { value: 'flaky' } })
    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE, 'ses_b'])
  })

  // The moment those two rows matter most is the moment a filter would hide
  // them: a user whose session is not here types its title, sees nothing match,
  // and needs "Enter a session ID..." right then. Filtered away, the only route
  // back was to clear the query first.
  it('keeps the rows that leave the list, whatever the query matches', () => {
    renderSelect({ sessions: [summary({ sessionId: 'ses_a', title: 'Refactor the parser' })] })

    fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'zzz no match' } })

    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE])
    expect(menuOptionLabels(MENU)).toEqual([NEW_SESSION_LABEL, typeAHandleLabel(false)])
  })

  // The age left the label for a column of its own. The filter reads both, so
  // the search the label used to serve still works.
  it('filters on the age as well as the title', () => {
    renderSelect({
      sessions: [
        summary({ sessionId: 'ses_recent', title: 'Recent', updatedAt: new Date().toISOString() }),
        summary({ sessionId: 'ses_ancient', title: 'Ancient', updatedAt: '2020-01-01T00:00:00.000Z' }),
      ],
    })
    fireEvent.input(screen.getByTestId('loading-menu-filter'), { target: { value: 'y ago' } })
    expect(menuOptionValues(MENU)).toEqual(['', TYPE_A_HANDLE_VALUE, 'ses_ancient'])
  })

  // The requirement the two columns exist for: a title with no length limit
  // must not be able to push the timestamp out of the row.
  it('keeps the age beside a title far too long for the row', () => {
    const title = Array.from({ length: 40 }, (_, i) => `Refactor step ${i}`).join(', ')
    renderSelect({ sessions: [summary({ sessionId: 'ses_long', title })] })

    const row = screen.getByTestId('loading-menu-option-ses_long')
    expect(row.textContent).toContain('ago')
    // The label clips; the detail beside it does not, so the two are separate
    // elements and only the first carries the clipping style.
    const label = screen.getByTestId('loading-menu-option-ses_long-label')
    expect(label.textContent).toBe(title)
    expect(label.className.split(/\s+/)).toContain(clippedText)
    expect(label.contains(screen.getByText(/ago/))).toBe(false)
  })

  // A clipped title is unreadable, and the tooltip is the only way back to it.
  it('gives the whole title on hover once the row clips it', () => {
    vi.useFakeTimers()
    try {
      const title = 'Teach the parser to accept a trailing comma in every list'
      renderSelect({ sessions: [summary({ sessionId: 'ses_long', title })] })
      openMenu(MENU)

      const label = screen.getByTestId('loading-menu-option-ses_long-label')
      stubClipped(label)
      expect(hoverForTooltip(label)?.textContent).toBe(title)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('shows the title alone in the option labels', () => {
    renderSelect({ sessions: [summary({ sessionId: 'ses_a', title: 'Newest' })] })
    expect(menuOptionLabels(MENU)).toEqual([NEW_SESSION_LABEL, typeAHandleLabel(false), 'Newest'])
  })

  it('names itself for a screen reader with the field label', () => {
    renderSelect()
    expect(screen.getAllByLabelText('Resume an existing session').length).toBeGreaterThan(0)
  })
})
