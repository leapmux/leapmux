import { timestampDate, timestampFromDate } from '@bufbuild/protobuf/wkt'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ElevationStatus } from './ElevationStatus'

const mockElevationExpiresAt = vi.fn<() => ReturnType<typeof timestampFromDate> | undefined>(() => undefined)
const mockSetElevationExpiresAt = vi.fn()
const mockDropElevation = vi.fn()
const mockRefreshUser = vi.fn<() => Promise<void>>()

vi.mock('~/context/AuthContext', () => ({
  useAuth: () => ({
    elevationExpiresAt: mockElevationExpiresAt,
    setElevationExpiresAt: mockSetElevationExpiresAt,
    refreshUser: mockRefreshUser,
  }),
}))

vi.mock('~/lib/elevation', async () => {
  const actual = await vi.importActual<typeof import('~/lib/elevation')>('~/lib/elevation')
  return { ...actual, dropElevation: (...args: unknown[]) => mockDropElevation(...args) }
})

const inTwoHours = () => timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
const anHourAgo = () => timestampFromDate(new Date(Date.now() - 60 * 60 * 1000))

describe('elevationStatus', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockDropElevation.mockResolvedValue(undefined)
    mockRefreshUser.mockResolvedValue(undefined)
  })

  it('shows nothing when the session is not verified', () => {
    mockElevationExpiresAt.mockReturnValue(undefined)
    render(() => <ElevationStatus />)
    expect(screen.queryByTestId('elevation-status')).not.toBeInTheDocument()
  })

  // The deadline is compared at the point of use, so a window that closed
  // while the page was open renders as absent rather than as a past date.
  it('shows nothing when the window already closed', () => {
    mockElevationExpiresAt.mockReturnValue(anHourAgo())
    render(() => <ElevationStatus />)
    expect(screen.queryByTestId('elevation-status')).not.toBeInTheDocument()
  })

  it('shows the state while the session is verified', () => {
    mockElevationExpiresAt.mockReturnValue(inTwoHours())
    render(() => <ElevationStatus />)
    expect(screen.getByTestId('elevation-status')).toBeInTheDocument()
  })

  // The row renders the SAME instant the hub enforces, through the same
  // converter every other reader of a protobuf Timestamp uses.
  //
  // A hand-rolled seconds-times-1000 dropped `nanos`, so a deadline with a
  // sub-second part rendered up to a second early -- and `isElevationCurrent`,
  // which does use timestampDate, would still call the window live. The two
  // readers of one timestamp on one page would then disagree.
  it('renders the deadline with sub-second precision, like the hub enforces it', () => {
    const withNanos = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    // A deadline nine tenths of a second past its whole second: truncating
    // it moves the rendered time into the previous second.
    withNanos.nanos = 900_000_000
    mockElevationExpiresAt.mockReturnValue(withNanos)

    render(() => <ElevationStatus />)

    const expected = timestampDate(withNanos).toLocaleTimeString()
    expect(screen.getByTestId('elevation-status')).toHaveTextContent(expected)
  })

  // Ending the window is the one action that never needs a privilege: it
  // only reduces access, and somebody stepping away from a shared machine
  // has a reason not to wait two hours.
  it('ends the window and adopts the new state', async () => {
    mockElevationExpiresAt.mockReturnValue(inTwoHours())
    render(() => <ElevationStatus />)

    fireEvent.click(screen.getByTestId('elevation-drop'))

    await vi.waitFor(() => {
      expect(mockDropElevation).toHaveBeenCalledTimes(1)
    })
    expect(mockSetElevationExpiresAt).toHaveBeenCalledWith(undefined)
  })

  // The window closes on its own, and nothing writes the signal when it
  // does: a slide emits no user_info event, so the client's copy stops
  // changing. Reading `new Date()` with no tracked dependency meant the row
  // was decided once at render and never again -- it kept offering "End now"
  // for a window that had already ended.
  it('stops rendering once the window lapses while the panel is open', async () => {
    vi.useFakeTimers()
    try {
      const inTenSeconds = timestampFromDate(new Date(Date.now() + 10_000))
      mockElevationExpiresAt.mockReturnValue(inTenSeconds)
      render(() => <ElevationStatus />)
      expect(screen.getByTestId('elevation-status')).toBeInTheDocument()

      // Past the deadline, and past one tick of the shared ticker.
      await vi.advanceTimersByTimeAsync(61_000)

      expect(screen.queryByTestId('elevation-status')).not.toBeInTheDocument()
    }
    finally {
      vi.useRealTimers()
    }
  })

  // The mirror goes stale EARLY, and only in that direction: the hub slides
  // the window forward on every gated action and emits no user_info event for
  // it, so a client that adopted 12:00 and then renamed a passkey at 11:55
  // keeps 12:00 while the hub holds 13:55. Hiding the row at 12:00 takes away
  // the "End now" button -- the only client of DropElevation -- while every
  // sensitive action still lands with no prompt.
  it('re-reads the account once when the mirrored deadline lapses', async () => {
    vi.useFakeTimers()
    try {
      const inTenSeconds = timestampFromDate(new Date(Date.now() + 10_000))
      mockElevationExpiresAt.mockReturnValue(inTenSeconds)
      render(() => <ElevationStatus />)
      expect(mockRefreshUser).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(61_000)
      expect(mockRefreshUser).toHaveBeenCalledTimes(1)

      // ONCE for the same deadline. A hub that answers with no later one
      // must not be asked again on every tick.
      await vi.advanceTimersByTimeAsync(61_000)
      expect(mockRefreshUser).toHaveBeenCalledTimes(1)
    }
    finally {
      vi.useRealTimers()
    }
  })

  // A window that was never open has nothing to confirm, so the row costs no
  // request at all on the common path.
  it('asks nothing when there is no deadline to confirm', async () => {
    vi.useFakeTimers()
    try {
      mockElevationExpiresAt.mockReturnValue(undefined)
      render(() => <ElevationStatus />)
      await vi.advanceTimersByTimeAsync(61_000)
      expect(mockRefreshUser).not.toHaveBeenCalled()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('reports a failed drop and keeps the state', async () => {
    mockElevationExpiresAt.mockReturnValue(inTwoHours())
    mockDropElevation.mockRejectedValue(new Error('the hub is unreachable'))
    render(() => <ElevationStatus />)

    fireEvent.click(screen.getByTestId('elevation-drop'))

    expect(await screen.findByText('the hub is unreachable')).toBeInTheDocument()
    expect(mockSetElevationExpiresAt).not.toHaveBeenCalled()
  })
})
