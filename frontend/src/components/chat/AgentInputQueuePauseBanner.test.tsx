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

  it('describes the state, never a cause, when the Worker names no reason', () => {
    // Inventing a cause is worse than describing the state, so UNSPECIFIED
    // reads exactly as the manual pause does.
    expect(pauseSentence(AgentInputQueuePauseReason.UNSPECIFIED))
      .toBe(pauseSentence(AgentInputQueuePauseReason.MANUAL))
    expect(pauseSentence(AgentInputQueuePauseReason.UNSPECIFIED)).not.toContain('because')
  })

  it('falls back to the manual sentence for a reason this build does not know', () => {
    // A Worker ahead of this client sends an enum value the generated table has
    // no key for. The banner must still say something.
    expect(pauseSentence(99 as AgentInputQueuePauseReason))
      .toBe(pauseSentence(AgentInputQueuePauseReason.MANUAL))
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
    // `onResume` is optional, and AgentEditorPanel leaves it unset whenever the
    // panel has no `onSetQueuePaused`. The press must not throw there.
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
})
