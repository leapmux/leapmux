import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentInputQueuePauseReason } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentInputQueuePauseBanner, pauseSentence } from './AgentInputQueuePauseBanner'

describe('agentInputQueuePauseBanner', () => {
  it('renders nothing while the queue runs', () => {
    render(() => (
      <AgentInputQueuePauseBanner paused={false} reason={AgentInputQueuePauseReason.MANUAL} />
    ))
    expect(screen.queryByTestId('queue-pause-banner')).not.toBeInTheDocument()
  })

  it('shows the banner with an empty queue, which is the case with no other signal', () => {
    render(() => (
      <AgentInputQueuePauseBanner paused reason={AgentInputQueuePauseReason.INTERRUPTED} />
    ))
    expect(screen.getByTestId('queue-pause-banner')).toHaveTextContent(
      'Queue paused because you interrupted the agent.',
    )
  })

  it('states the cause for every automatic pause reason', () => {
    const cases: [AgentInputQueuePauseReason, string][] = [
      [AgentInputQueuePauseReason.INTERRUPTED, 'you interrupted the agent'],
      [AgentInputQueuePauseReason.AGENT_STOPPED, 'the agent stopped'],
      [AgentInputQueuePauseReason.DELIVERY_FAILED, 'did not reach the agent'],
      [AgentInputQueuePauseReason.DELIVERY_UNCERTAIN, 'may not have reached the agent'],
    ]
    for (const [reason, fragment] of cases)
      expect(pauseSentence(reason)).toContain(fragment)
  })

  it('describes the state, never a cause, when the Worker gives no reason', () => {
    // Inventing a cause is worse than describing the state, so UNSPECIFIED
    // reads exactly as the manual pause does.
    expect(pauseSentence(AgentInputQueuePauseReason.UNSPECIFIED))
      .toBe(pauseSentence(AgentInputQueuePauseReason.MANUAL))
    expect(pauseSentence(AgentInputQueuePauseReason.UNSPECIFIED)).not.toContain('because')
  })

  it('falls back to the UNSPECIFIED sentence for a reason this build does not know', () => {
    // A Worker ahead of this client sends an enum value the generated table has
    // no key for. The banner must still say something, and UNSPECIFIED is
    // already the "the Worker told us nothing" bucket.
    expect(pauseSentence(99 as AgentInputQueuePauseReason))
      .toBe(pauseSentence(AgentInputQueuePauseReason.UNSPECIFIED))
  })

  it('resumes from its own button', async () => {
    const onResume = vi.fn()
    render(() => (
      <AgentInputQueuePauseBanner paused reason={AgentInputQueuePauseReason.MANUAL} onResume={onResume} />
    ))
    await fireEvent.click(screen.getByRole('button', { name: 'Resume' }))
    expect(onResume).toHaveBeenCalledTimes(1)
  })

  it('survives a Resume press with no handler attached', () => {
    // `onResume` is optional so this component renders standalone here. The one
    // production caller always supplies it, so the press must not throw for a
    // test that does not.
    render(() => (
      <AgentInputQueuePauseBanner paused reason={AgentInputQueuePauseReason.MANUAL} />
    ))
    expect(() => fireEvent.click(screen.getByRole('button', { name: 'Resume' }))).not.toThrow()
  })

  it('keeps the live region mounted while the queue runs, so a later pause announces', () => {
    const { container } = render(() => (
      <AgentInputQueuePauseBanner paused={false} reason={AgentInputQueuePauseReason.MANUAL} />
    ))
    // A live region that appears together with its text announces nothing on
    // several screen readers, and four of the five reasons arrive unprompted.
    const live = container.querySelector('[role="status"]')
    expect(live).not.toBeNull()
    expect(live).toHaveTextContent('')
  })

  it('puts the sentence in the live region once paused', () => {
    const { container } = render(() => (
      <AgentInputQueuePauseBanner paused reason={AgentInputQueuePauseReason.AGENT_STOPPED} />
    ))
    expect(container.querySelector('[role="status"]')).toHaveTextContent(
      'Queue paused because the agent stopped.',
    )
  })

  it('holds the sentence in ONE node, so a screen reader hears it once', () => {
    const sentence = 'Queue paused because the agent stopped.'
    const { container } = render(() => (
      <AgentInputQueuePauseBanner paused reason={AgentInputQueuePauseReason.AGENT_STOPPED} />
    ))
    // A second copy beside the live region reaches the accessibility tree too,
    // so the reader hears the sentence twice and a by-text lookup resolves two
    // elements. The visible banner IS the live region here; only its class
    // swaps.
    const carriers = [...container.querySelectorAll('*')].filter(el => el.textContent === sentence)
    expect(carriers).toHaveLength(1)
    expect(container.querySelector('[role="status"]')).toContainElement(carriers[0] as HTMLElement)
  })

  it('keeps the live region out of flow while the queue runs, so it takes no gap', () => {
    const { container } = render(() => (
      <AgentInputQueuePauseBanner paused={false} reason={AgentInputQueuePauseReason.MANUAL} />
    ))
    // `inputArea` is a flex column with a `gap`, so an in-flow empty child
    // would open a gap above the queue. `srOnly` is `position: absolute`, which
    // takes the node out of that flow. It also carries no test id while the
    // queue runs, so a locator cannot resolve a banner that is not showing.
    const live = container.querySelector('[role="status"]')!
    expect(live.className).toContain('srOnly')
    expect(live).not.toHaveAttribute('data-testid')
  })
})
