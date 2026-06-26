/**
 * Scroll-velocity tracker: tells an inertial FLING (fast, real momentum a
 * scrollTop write would cancel) apart from a slow DELIBERATE scroll (no momentum,
 * safe to correct immediately). repinToAnchor consults `isFling` to decide
 * whether to defer a small correction into flingSettle. It biases toward "fling"
 * so the prior always-defer behavior is the default: an UNKNOWN velocity (the
 * first event of a gesture, no prior sample) or a STALE one (the last sample is
 * older than `idleMs`, so momentum has since stopped) both report a fling. Only a
 * freshly-sampled, below-threshold cadence -- an established slow scroll -- yields
 * a non-fling and an immediate correction. `now` is injected so the unit is
 * deterministically testable. Extracted from the scroll hook, mirroring
 * createFlingSettle.
 */
export function createScrollVelocity(deps: {
  now: () => number
  thresholdPxPerMs: number
  idleMs: number
}) {
  let seeded = false
  let lastPos = 0
  let lastTime = 0
  // The time of the last PROGRAMMATIC write (syncToProgrammatic), or -1 when none is
  // pending. It re-baselines the NEXT velocity sample's interval without disturbing
  // `lastTime` (which the idle checks read). Cleared by the next real sample.
  let lastWriteTime = -1
  // px/ms over the last inter-event gap; Infinity until two samples establish it.
  let velocity = Number.POSITIVE_INFINITY
  return {
    /** Record a scroll event at position `pos` (px). Call once per real (non-echo) scroll. */
    sample(pos: number) {
      const t = deps.now()
      if (seeded) {
        // Measure velocity over the gap since the most recent baseline-setting event:
        // a real sample OR a programmatic write (whichever is later). A write advanced
        // lastPos to the post-write position, so the next sample's displacement is the
        // user's real movement since the write -- pairing it with the write TIME (not
        // the older last-sample time) stops the write from inflating dt and
        // under-measuring a fresh fling into a FALSE non-fling that cancels momentum.
        const baseTime = Math.max(lastTime, lastWriteTime)
        const dt = t - baseTime
        // A same-tick coalesced event (dt <= 0) carries no measurable interval, so
        // it can't bound a speed. DROP it without moving the baseline -- rather than
        // dividing by zero, AND rather than the old behavior of absorbing its
        // position jump (which left lastTime fresh but under-measured the NEXT
        // interval's velocity from the post-jump position). The next real sample then
        // measures the average over the whole span from the last TIMED baseline, and
        // -- because lastTime is unchanged -- the wall-clock idle check
        // (now() - lastTime > idleMs in isFling / isActivelyFlinging / speed) keeps
        // deciding stopped-vs-coasting from the last MEASURABLE real sample, so a fling
        // tail that coalesces can't hold a stale-high velocity past its real momentum.
        if (dt <= 0)
          return
        velocity = Math.abs(pos - lastPos) / dt
      }
      seeded = true
      lastPos = pos
      lastTime = t
      // A real sample supersedes any pending programmatic-write baseline.
      lastWriteTime = -1
    },
    /**
     * Advance the position baseline to `pos` for a PROGRAMMATIC scroll write (a re-pin
     * / settle / stick) whose displacement is NOT a user gesture, and record the write
     * TIME so the next real `sample` measures the user's delta over the interval since
     * the write rather than since the stale last real sample. `lastTime` is left
     * untouched so the idle checks keep measuring from the last REAL sample -- a write
     * is not user activity and must not look "recent" to them. Without this, a re-pin
     * (which fires constantly in a hidden-heavy window, where each near-edge load
     * prepends/appends and re-pins) would inflate the next velocity into a FALSE fling
     * that defers corrections into flingSettle, where they accumulate and land as one
     * overshoot; under-measuring it (the prior bug) cancelled a real fling's momentum.
     * No-op until a real sample has seeded the baseline.
     */
    syncToProgrammatic(pos: number) {
      if (seeded) {
        lastPos = pos
        lastWriteTime = deps.now()
      }
    },
    /** Whether the current scroll looks like an inertial fling (defer) vs a slow scroll (apply now). */
    isFling(): boolean {
      if (!seeded)
        return true
      if (deps.now() - lastTime > deps.idleMs)
        return true
      return velocity >= deps.thresholdPxPerMs
    },
    /**
     * Whether a fling's momentum is ACTIVELY in progress right now: a recent real
     * sample (within idleMs) carrying a MEASURED, fling-speed velocity. Differs
     * from isFling() in two ways that make it safe for an ASYNC re-pin (one firing
     * outside a scroll event, where isFling's "unknown/idle => defer" default would
     * misfire): it is FALSE while idle (momentum stopped -> a write is safe) and
     * FALSE for the unknown (Infinity) seed before two samples establish a speed
     * (so a single-sample/cold start keeps correcting immediately rather than being
     * mistaken for a live fling). True only for a genuine, measured, in-flight
     * fling whose momentum a scrollTop write would cancel.
     */
    isActivelyFlinging(): boolean {
      if (!seeded)
        return false
      if (deps.now() - lastTime > deps.idleMs)
        return false
      return Number.isFinite(velocity) && velocity >= deps.thresholdPxPerMs
    },
    /**
     * The current scroll speed in px/ms, for the render-ahead overscan that paints
     * the rows a fast fling is about to reach. Mirrors isActivelyFlinging's gating:
     * 0 while idle (momentum stopped) or on the unknown Infinity seed (cold start),
     * so a stale or unknown speed yields NO look-ahead rather than an unbounded one.
     */
    speed(): number {
      if (!seeded)
        return 0
      if (deps.now() - lastTime > deps.idleMs)
        return 0
      return Number.isFinite(velocity) ? velocity : 0
    },
  }
}
