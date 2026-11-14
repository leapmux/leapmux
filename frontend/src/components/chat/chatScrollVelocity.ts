/**
 * How long (ms) after the last scroll sample the speed reads as 0: the scroll stopped.
 * The scroll hook also clears its fast-scroll flag after this long.
 */
export const VELOCITY_IDLE_MS = 150

/**
 * Scroll-speed tracker for the scroll hook. The speed sizes the render-ahead lead of a
 * fast scroll (the rows it is about to reach mount ahead of it) and raises the fast-scroll
 * flag that mounts entering rows as skeletons. `now` is injected so the unit is
 * deterministically testable.
 */
export function createScrollVelocity(deps: {
  now: () => number
  idleMs: number
}) {
  let seeded = false
  let lastPos = 0
  let lastTime = 0
  // The time of the last PROGRAMMATIC write (syncToProgrammatic), or -1 when none is
  // pending. It re-baselines the NEXT velocity sample's interval without disturbing
  // `lastTime` (which the idle check reads). Cleared by the next real sample.
  let lastWriteTime = -1
  // px/ms over the last inter-event gap; Infinity until two samples establish it.
  let velocity = Number.POSITIVE_INFINITY
  return {
    /** Record a scroll event at position `pos` (px). Call once per move of the reader. */
    sample(pos: number) {
      const t = deps.now()
      if (seeded) {
        // Measure velocity over the gap since the most recent baseline-setting event:
        // a real sample OR a programmatic write (whichever is later). A write advanced
        // lastPos to the post-write position, so the next sample's displacement is the
        // reader's real movement since the write -- pairing it with the write TIME (not
        // the older last-sample time) keeps the write from inflating dt and
        // under-measuring the speed.
        const baseTime = Math.max(lastTime, lastWriteTime)
        const dt = t - baseTime
        // A same-tick coalesced event (dt <= 0) carries no measurable interval, so it
        // can't bound a speed. DROP it without moving the baseline, rather than dividing
        // by zero. The next real sample then measures the average over the whole span
        // from the last TIMED baseline, and the idle check keeps deciding from the last
        // MEASURABLE real sample.
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
     * Advance the position baseline to `pos` for a PROGRAMMATIC scroll write (a stick, a
     * placement, a rebase) whose displacement is NOT a move of the reader, and record the
     * write TIME so the next real `sample` measures the reader's delta over the interval
     * since the write rather than since the stale last real sample. `lastTime` is left
     * untouched so the idle check keeps measuring from the last REAL sample -- a write is
     * not reader activity and must not look "recent" to it. No-op until a real sample
     * has seeded the baseline.
     */
    syncToProgrammatic(pos: number) {
      if (seeded) {
        lastPos = pos
        lastWriteTime = deps.now()
      }
    },
    /**
     * The current scroll speed in px/ms: 0 before two samples establish it, and 0 when
     * the last sample is older than `idleMs` (the scroll stopped), so a stale or unknown
     * speed yields no look-ahead rather than an unbounded one.
     */
    speed(): number {
      if (!seeded)
        return 0
      if (deps.now() - lastTime > deps.idleMs)
        return 0
      return Number.isFinite(velocity)
        ? velocity
        : 0
    },
  }
}
