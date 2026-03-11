export interface CompassState {
  angle: number
}

export function createCompassSimulation(
  onUpdate: (state: CompassState) => void,
): { start: () => void, stop: () => void } {
  const SPRING_K = 3.0
  const DAMPING = 2.0
  const TICK_MS = 67 // ~15 FPS
  const MAX_DT = 0.1

  let angle = 0
  let angularVelocity = 0
  let target = 0
  let nextTargetTime = 0
  let lastTime = 0
  let intervalId: ReturnType<typeof setInterval> | undefined

  function pickNewTarget(now: number) {
    const offset = (Math.PI / 4 + Math.random() * Math.PI * 0.75) * (Math.random() < 0.5 ? 1 : -1)
    target = angle + offset
    nextTargetTime = now + 1.5 + Math.random() * 2.0
  }

  function tick() {
    const now = performance.now() / 1000
    if (lastTime === 0) {
      lastTime = now
      pickNewTarget(now)
    }

    const dt = Math.min(now - lastTime, MAX_DT)
    lastTime = now

    if (now >= nextTargetTime) {
      pickNewTarget(now)
    }

    const torque = -SPRING_K * (angle - target) - DAMPING * angularVelocity
    angularVelocity += torque * dt
    angle += angularVelocity * dt

    onUpdate({ angle })
  }

  return {
    start() {
      if (intervalId !== undefined)
        return
      lastTime = 0
      intervalId = setInterval(tick, TICK_MS)
      tick()
    },
    stop() {
      if (intervalId !== undefined) {
        clearInterval(intervalId)
        intervalId = undefined
      }
    },
  }
}
