import type { Component } from 'solid-js'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createEffect, createSignal, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { useAuth } from '~/context/AuthContext'
import { createSharedTicker } from '~/lib/createSharedTicker'
import { dropElevation, isElevationCurrent } from '~/lib/elevation'
import { formatErrorMessage } from '~/lib/errors'
import { errorText } from '~/styles/shared.css'
import * as styles from './ElevationStatus.css'

/**
 * How often the row re-reads the clock.
 *
 * The window is two hours and the row shows a wall-clock time to the minute,
 * so a minute is as precise as anything it renders. The ticker runs only
 * while a subscriber is mounted, which for this row means only while
 * Preferences is open.
 */
const ELEVATION_TICK_MS = 60_000

const elevationTick = createSharedTicker(ELEVATION_TICK_MS)

/**
 * Shows that the session is verified, and ends the window on demand.
 *
 * The deadline is the only elevation state a surface may render, and
 * `AuthState.elevationExpiresAt` exists for exactly this: it is a timestamp
 * rather than a boolean because a boolean decided at bootstrap is wrong for
 * the rest of the page's life. Nothing here DECIDES anything -- the hub
 * refuses an un-elevated action on its own -- so a stale value can only
 * render a row a moment too long, never admit a change.
 *
 * The control exists because the window is otherwise only endable by waiting
 * two hours. Somebody stepping away from a shared machine has a reason to
 * end it now, and revoking a privilege is the one action that never needs a
 * privilege.
 */
export const ElevationStatus: Component = () => {
  const auth = useAuth()
  const [busy, setBusy] = createSignal(false)
  const [message, setMessage] = createSignal('')

  // The deadline passes on its own, and nothing writes the signal when it
  // does: a slide deliberately emits no user_info event, so the client's
  // copy stops changing. Without a tick, `new Date()` inside
  // isElevationCurrent is read once and never again, and the row keeps
  // saying "verified until 14:30" with a live "End now" button hours later
  // -- on the one screen the docs point somebody at when they step away
  // from a shared machine.
  elevationTick.subscribe()

  const until = () => auth.elevationExpiresAt()

  const elevated = () => {
    elevationTick.tick()
    return isElevationCurrent(until())
  }

  /**
   * The deadline this row already re-read once, at its lapse.
   *
   * A plain reference rather than a signal: it must not be reactive, or
   * writing it would re-run the effect that wrote it. Keying on the DEADLINE
   * means a newly adopted one re-arms the check, and a hub that answers with
   * no later deadline does not loop, because the same value lapses only once.
   */
  let confirmedLapseOf: string | null = null

  /**
   * Re-read the account ONCE when the mirrored deadline lapses.
   *
   * The mirror goes stale EARLY, and only in that direction. The hub SLIDES
   * the window forward on every gated action and deliberately emits no
   * user_info event for it, so a client that adopted a deadline of 12:00 and
   * then renamed a passkey at 11:55 keeps 12:00 while the hub holds 13:55.
   * At 12:00 this row would unmount, and with it the "End now" button — the
   * only client of DropElevation, and the control the docs point at for "I
   * am stepping away from a shared machine" — while every sensitive action
   * still landed with no prompt.
   *
   * Re-reading HERE rather than at each gated call site is what makes it hold
   * for the call sites that do not refresh today and for the ones nobody has
   * written yet: three of them refresh the account for their own reasons, and
   * AccountPasskeys' rename deliberately does not, because a rename changes
   * nothing else. One request per lapse is the whole cost.
   *
   * In an EFFECT, not in the predicate above: the predicate runs inside
   * Show's memo, and a signal write from there is a write during a
   * computation.
   */
  createEffect(() => {
    const deadline = until()
    elevationTick.tick()
    if (!deadline || isElevationCurrent(deadline))
      return
    const key = `${deadline.seconds}.${deadline.nanos}`
    if (confirmedLapseOf === key)
      return
    confirmedLapseOf = key
    void auth.refreshUser()
  })

  const end = () => {
    setBusy(true)
    setMessage('')
    void (async () => {
      try {
        await dropElevation()
        // Adopt the new state locally rather than re-reading the account:
        // the hub just told us the window is gone, and the drop is the one
        // change that must never be reported as still live.
        auth.setElevationExpiresAt(undefined)
      }
      catch (e) {
        setMessage(formatErrorMessage(e, 'Could not end the verification'))
      }
      finally {
        setBusy(false)
      }
    })()
  }

  return (
    <Show when={elevated()}>
      {/*
        The SAME Alert the panel's restart warning uses, and for the same
        reason: both are one sentence about the group the reader is looking at,
        and two boxes drawn two ways read as two kinds of thing. This one is
        not a warning, so it keeps the default (informational) variant.
      */}
      <Alert>
        {/*
          ONE child of the alert, which is itself a flex row: a second child
          would sit BESIDE the first rather than beneath it, so the failure
          message would land next to the sentence it belongs under.
        */}
        <div class={styles.body} data-testid="elevation-status">
          <div class={styles.row}>
            <div class={styles.text}>
              <strong>This session is verified.</strong>
              {' '}
              {/*
                Through timestampDate, like every other reader of a protobuf
                Timestamp here. The hand-rolled seconds-times-1000 dropped
                nanos, so this row could render an instant up to a second
                earlier than the one isElevationCurrent -- which does use
                timestampDate -- compares against, and the panel would say a
                window had ended while the same component still treated it as
                live.
              */}
              <Show when={until()}>
                {ts => (
                  <>
                    Sensitive changes land without another prompt until
                    {' '}
                    {timestampDate(ts()).toLocaleTimeString()}
                    .
                  </>
                )}
              </Show>
            </div>
            <button type="button" class="small outline" onClick={end} disabled={busy()} data-testid="elevation-drop">
              {busy() ? 'Ending...' : 'End now'}
            </button>
          </div>
          {/*
            No `role="alert"` of its own: the Alert around it already IS one,
            and a live region inside a live region announces twice. The
            failure is still announced, because adding content to an open
            `role="alert"` box re-announces the box.
          */}
          <Show when={message()}>
            <div class={errorText}>{message()}</div>
          </Show>
        </div>
      </Alert>
    </Show>
  )
}
