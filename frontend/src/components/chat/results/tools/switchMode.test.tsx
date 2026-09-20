import { beforeAll, describe, expect, it } from 'vitest'
import { checkKindModule } from '~/test-support/kindTestHarness'
import { toolCallFixture } from '~/test-support/toolCallFixture'
import { parsedCall } from './renderer'
import { switchModeRenderer } from './switchMode'

// jsdom does not provide ResizeObserver, which the shared layouts observe with.
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

describe('switch_mode renderer', () => {
  checkKindModule({
    kind: 'switch_mode',
    request: { mode: 'plan' },
    titlePart: 'plan',
    result: { text: 'Switched.', format: 'plain' },
    resultPart: 'Switched.',
  })

  // The prose factory supplies the body and the toolbar; a kind's OWN hooks ride
  // beside them. A spread that dropped this one would word a refused plan as a
  // failure, which is the opposite of what the agent asked for.
  it('takes the words a refusal states from the request', () => {
    const declined = toolCallFixture('switch_mode', {
      status: 'declined',
      request: { declinedTitle: 'Sent feedback' },
      result: { text: 'Not yet', format: 'plain' },
    })
    expect(switchModeRenderer.outcomeTitle?.(parsedCall(declined))).toBe('Sent feedback')
  })

  // This layer knows no provider, so a refusal that words nothing takes the shared
  // outcome word. Spelling "Sent feedback" here stated ONE tool's semantics -- Claude's
  // `ExitPlanMode` -- over every provider's mode switch.
  it('words a refusal that states nothing the way the shared header does', () => {
    const declined = toolCallFixture('switch_mode', { status: 'declined', result: { text: 'Not yet', format: 'plain' } })
    expect(switchModeRenderer.outcomeTitle?.(parsedCall(declined))).toBe('Declined')
  })

  it('words every other outcome the way the shared header does', () => {
    const failed = toolCallFixture('switch_mode', { status: 'failed', result: { text: 'boom', format: 'plain' } })
    expect(switchModeRenderer.outcomeTitle?.(parsedCall(failed))).toBe('Error')
  })

  // A request that words a refusal states nothing about any other outcome.
  it('leaves an outcome that is not a refusal to the shared header', () => {
    const failed = toolCallFixture('switch_mode', {
      status: 'failed',
      request: { declinedTitle: 'Sent feedback' },
      result: { text: 'boom', format: 'plain' },
    })
    expect(switchModeRenderer.outcomeTitle?.(parsedCall(failed))).toBe('Error')
  })
})
