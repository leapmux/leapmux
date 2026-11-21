import type { InputQueueAdvanceSteps } from './advanceInputQueue'
import type { QueueHeadAction } from './agentInputQueueDrop'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { AgentInputQueuePauseReason, AgentInputState, QueuedAgentInputSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { advanceInputQueue } from './advanceInputQueue'

const h1 = create(QueuedAgentInputSchema, { id: 'h1' })
const h2 = create(QueuedAgentInputSchema, { id: 'h2' })

const steerOn = (head = h1): QueueHeadAction => ({ action: 'steer', head })
const preemptOn = (head = h1): QueueHeadAction => ({ action: 'preempt', head })

/** The queue and the panel as the steps read them. Each effect below changes it. */
interface World {
  reason: AgentInputQueuePauseReason
  headId: string | undefined
  action: QueueHeadAction | undefined
  states: Record<string, AgentInputState>
  current: boolean
}

interface Effects {
  /** What the Worker's answer to the action applies before `act` resolves. */
  afterAct?: (world: World) => void
  /** What the Worker's answer to the resume applies before `resume` resolves. */
  afterResume?: (world: World) => void
  actResult?: boolean
  resumeResult?: boolean
}

/**
 * Steps over one mutable world. `calls` records every action and resume in
 * order, as `<action>:<head id>` or `resume`, so each case asserts the order
 * and the count in one comparison.
 */
function setup(initial: Partial<World>, effects: Effects = {}) {
  const world: World = {
    reason: AgentInputQueuePauseReason.UNSPECIFIED,
    headId: 'h1',
    action: undefined,
    states: {},
    current: true,
    ...initial,
  }
  const calls: string[] = []
  const steps: InputQueueAdvanceSteps = {
    headAction: () => world.action,
    headId: () => world.headId,
    inputState: inputId => world.states[inputId],
    pauseReason: () => world.reason,
    stillCurrent: () => world.current,
    act: vi.fn(async (offered: QueueHeadAction) => {
      calls.push(`${offered.action}:${offered.head.id}`)
      effects.afterAct?.(world)
      return effects.actResult ?? true
    }),
    resume: vi.fn(async () => {
      calls.push('resume')
      effects.afterResume?.(world)
      return effects.resumeResult ?? true
    }),
  }
  return {
    calls,
    run: () => advanceInputQueue(steps),
  }
}

describe('advanceInputQueue', () => {
  describe('on a running queue', () => {
    it('acts on the offered head and never resumes', async () => {
      const { calls, run } = setup({
        action: steerOn(),
      })
      await run()
      expect(calls).toEqual(['steer:h1'])
    })

    it('does nothing when the head offers nothing', async () => {
      const { calls, run } = setup({})
      await run()
      expect(calls).toEqual([])
    })

    // The queue was running at the keypress, so the user saw no pause.
    it('does not resume a pause that the steer itself caused', async () => {
      const { calls, run } = setup({
        action: steerOn(),
      }, {
        afterAct: (world) => { world.reason = AgentInputQueuePauseReason.DELIVERY_FAILED },
      })
      await run()
      expect(calls).toEqual(['steer:h1'])
    })
  })

  describe('on a paused queue', () => {
    const paused = { reason: AgentInputQueuePauseReason.MANUAL }

    it('steers first, then resumes', async () => {
      const { calls, run } = setup({
        ...paused,
        action: steerOn(),
      })
      await run()
      expect(calls).toEqual(['steer:h1', 'resume'])
    })

    it('still resumes after a steer that fails, and does not steer again', async () => {
      const { calls, run } = setup({
        ...paused,
        action: steerOn(),
      }, {
        actResult: false,
        afterResume: (world) => { world.action = preemptOn() },
      })
      await run()
      expect(calls).toEqual(['steer:h1', 'resume'])
    })

    it('resumes, then preempts the head of the keypress', async () => {
      const { calls, run } = setup(paused, {
        afterResume: (world) => { world.action = preemptOn(h1) },
      })
      await run()
      expect(calls).toEqual(['resume', 'preempt:h1'])
    })

    // The resume's drain started a turn for h1, and h2 now offers a preempt
    // against that turn. Acting would cancel the turn that this press started.
    it('does not preempt another head after the resume', async () => {
      const { calls, run } = setup(paused, {
        afterResume: (world) => {
          world.headId = 'h2'
          world.action = preemptOn(h2)
        },
      })
      await run()
      expect(calls).toEqual(['resume'])
    })

    it('never steers after the resume', async () => {
      const { calls, run } = setup(paused, {
        afterResume: (world) => { world.action = steerOn(h1) },
      })
      await run()
      expect(calls).toEqual(['resume'])
    })

    it('only resumes when the head offers nothing before or after the resume', async () => {
      const { calls, run } = setup(paused)
      await run()
      expect(calls).toEqual(['resume'])
    })

    it('resumes an empty queue', async () => {
      const { calls, run } = setup({ ...paused, headId: undefined })
      await run()
      expect(calls).toEqual(['resume'])
    })

    // A resume refuses for a request already in flight, a login recovery that
    // opens its dialog instead, or a read-only composer.
    it('does not preempt when the resume did not happen', async () => {
      const { calls, run } = setup(paused, {
        resumeResult: false,
        afterResume: (world) => { world.action = preemptOn(h1) },
      })
      await run()
      expect(calls).toEqual(['resume'])
    })

    it('does not resume when the steer replaced the pause with a new reason', async () => {
      const { calls, run } = setup({
        ...paused,
        action: steerOn(),
      }, {
        afterAct: (world) => { world.reason = AgentInputQueuePauseReason.DELIVERY_FAILED },
      })
      await run()
      expect(calls).toEqual(['steer:h1'])
    })

    it.each([
      AgentInputState.FAILED,
      AgentInputState.DELIVERY_UNCERTAIN,
    ])('does not resume when a failed steer wrote a pause with the same reason (head state %s)', async (state) => {
      const { calls, run } = setup({
        reason: AgentInputQueuePauseReason.DELIVERY_FAILED,
        action: steerOn(),
      }, {
        afterAct: (world) => { world.states.h1 = state },
      })
      await run()
      expect(calls).toEqual(['steer:h1'])
    })

    it('resumes after a steer that the provider put back in the queue', async () => {
      const { calls, run } = setup({
        ...paused,
        action: steerOn(),
      }, {
        afterAct: (world) => { world.states.h1 = AgentInputState.QUEUED },
      })
      await run()
      expect(calls).toEqual(['steer:h1', 'resume'])
    })

    it('stops before the resume when the panel changes agents during the steer', async () => {
      const { calls, run } = setup({
        ...paused,
        action: steerOn(),
      }, {
        afterAct: (world) => { world.current = false },
      })
      await run()
      expect(calls).toEqual(['steer:h1'])
    })

    it('stops before the preempt when the panel changes agents during the resume', async () => {
      const { calls, run } = setup(paused, {
        afterResume: (world) => {
          world.current = false
          world.action = preemptOn(h1)
        },
      })
      await run()
      expect(calls).toEqual(['resume'])
    })
  })
})
