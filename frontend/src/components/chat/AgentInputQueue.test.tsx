import type { MessageInitShape } from '@bufbuild/protobuf'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import {
  AgentInputKind,
  AgentInputQueueSnapshotSchema,
  AgentInputState,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentInputQueue, resolveQueueDrop } from './AgentInputQueue'

function item(id: string, overrides: MessageInitShape<typeof QueuedAgentInputSchema> = {}) {
  return create(QueuedAgentInputSchema, {
    id,
    agentId: 'agent-1',
    text: `text ${id}`,
    kind: AgentInputKind.USER_MESSAGE,
    state: AgentInputState.QUEUED,
    ...overrides,
  })
}

function renderQueue(overrides: {
  items?: ReturnType<typeof item>[]
  supportsSteering?: boolean
  activeEditInputId?: string
} = {}) {
  const handlers = {
    onEdit: vi.fn(),
    onCancelEdit: vi.fn(),
    onDelete: vi.fn(),
    onMove: vi.fn(),
    onRetry: vi.fn(),
    onSteer: vi.fn(),
  }
  const snapshot = create(AgentInputQueueSnapshotSchema, {
    agentId: 'agent-1',
    items: overrides.items ?? [item('one'), item('two')],
  })
  render(() => (
    <AgentInputQueue
      snapshot={snapshot}
      clientId="client-a"
      activeEditInputId={overrides.activeEditInputId}
      supportsSteering={overrides.supportsSteering ?? false}
      {...handlers}
    />
  ))
  return handlers
}

// A pointer drag cannot be reproduced here: solid-dnd activates on real
// pointer geometry and collision detection, which jsdom does not supply. The
// gesture is covered in tests/e2e/108-agent-input-queue.spec.ts; the
// arithmetic it feeds is covered directly.
describe('resolveQueueDrop', () => {
  const three = [item('one'), item('two'), item('three')]

  it('points an upward drop at the drop target itself', () => {
    expect(resolveQueueDrop(three, 'qi-two', 'qi-one')).toEqual({
      moved: expect.objectContaining({ id: 'two' }),
      beforeInputId: 'one',
    })
  })

  it('points a downward drop at the item after the drop target', () => {
    // The removal shifts everything below up by one, so aiming at the target
    // itself would land the row one slot short.
    expect(resolveQueueDrop(three, 'qi-one', 'qi-two')).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: 'three',
    })
  })

  it('sends a drop on the last row to the end', () => {
    expect(resolveQueueDrop(three, 'qi-one', 'qi-three')).toEqual({
      moved: expect.objectContaining({ id: 'one' }),
      beforeInputId: '',
    })
  })

  it('refuses a drop on the dragged row itself', () => {
    expect(resolveQueueDrop(three, 'qi-two', 'qi-two')).toBeUndefined()
  })

  it('refuses an id that is not in the list', () => {
    expect(resolveQueueDrop(three, 'qi-nope', 'qi-one')).toBeUndefined()
    expect(resolveQueueDrop(three, 'qi-one', 'qi-nope')).toBeUndefined()
  })

  it('refuses to move a dispatching row', () => {
    const list = [item('one', { state: AgentInputState.DISPATCHING }), item('two')]
    expect(resolveQueueDrop(list, 'qi-one', 'qi-two')).toBeUndefined()
  })

  it('refuses to displace a dispatching row', () => {
    // The Worker is already sending it, so its slot is not the user's to take.
    const list = [item('one', { state: AgentInputState.DISPATCHING }), item('two')]
    expect(resolveQueueDrop(list, 'qi-two', 'qi-one')).toBeUndefined()
  })

  it('refuses an empty list', () => {
    expect(resolveQueueDrop([], 'qi-one', 'qi-two')).toBeUndefined()
  })
})

describe('agentInputQueue', () => {
  it('renders operation, state, text, and attachment metadata', () => {
    renderQueue({
      items: [item('compact', {
        text: '/compact',
        kind: AgentInputKind.COMPACT_CONTEXT,
        attachments: [{ $typeName: 'leapmux.v1.QueuedAgentInputAttachment', filename: 'a.txt', mimeType: 'text/plain', size: 4n, order: 0 }],
      })],
    })
    expect(screen.getByText('/compact')).toBeInTheDocument()
    expect(screen.getByText(/Compact context · Queued · a\.txt \(4 B\)/)).toBeInTheDocument()
  })

  it('moves an input up with the keyboard action', async () => {
    const handlers = renderQueue()
    const second = screen.getByTestId('queued-input-two')
    await fireEvent.click(within(second).getByRole('button', { name: 'Move Up' }))
    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), 'one')
  })

  // Move Down passes the item TWO slots below, because the Worker takes the
  // item out of the list before it looks the target up. `index + 1` would name
  // the item's own new position, which makes the button a silent no-op.
  it('moves an input down with the keyboard action', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two'), item('three')] })
    const first = screen.getByTestId('queued-input-one')
    await fireEvent.click(within(first).getByRole('button', { name: 'Move Down' }))
    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), 'three')
  })

  it('moves the second-to-last input to the end with the keyboard action', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two'), item('three')] })
    const second = screen.getByTestId('queued-input-two')
    await fireEvent.click(within(second).getByRole('button', { name: 'Move Down' }))
    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), '')
  })

  it('gives a dispatching row an inert grip, so it offers no drag it cannot do', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    const dispatching = screen.getByTestId('queue-drag-handle-one')
    const movable = screen.getByTestId('queue-drag-handle-two')
    // Hidden rather than removed: the grip keeps its grid cell, so the row's
    // text does not shift one column left.
    expect(dispatching.className).toContain('dragHandleInert')
    expect(movable.className).not.toContain('dragHandleInert')
  })

  it('does not move an item across a dispatching head', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    const second = screen.getByTestId('queued-input-two')
    expect(within(second).getByRole('button', { name: 'Move Up' })).toBeDisabled()
  })

  it('shows Take Over for an edit owned by another client', async () => {
    const handlers = renderQueue({ items: [item('one', { editOwnerClientId: 'client-b' })] })
    await fireEvent.click(screen.getByRole('button', { name: 'Take Over' }))
    expect(handlers.onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), true)
  })

  it('requires takeover before editing a different item', async () => {
    const handlers = renderQueue({
      items: [item('one', { editOwnerClientId: 'client-a' }), item('two')],
      activeEditInputId: 'one',
    })
    const second = screen.getByTestId('queued-input-two')

    await fireEvent.click(within(second).getByRole('button', { name: 'Take Over' }))

    expect(handlers.onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), true)
    expect(within(second).queryByRole('button', { name: /^Edit$/ })).not.toBeInTheDocument()
  })

  it('resumes an owned edit that this panel has not loaded', async () => {
    const handlers = renderQueue({ items: [item('one', { editOwnerClientId: 'client-a' })] })
    await fireEvent.click(screen.getByRole('button', { name: 'Resume Edit' }))
    expect(handlers.onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), false)
  })

  it('cancels the owned edit that this panel loaded', async () => {
    const handlers = renderQueue({
      items: [item('one', { editOwnerClientId: 'client-a' })],
      activeEditInputId: 'one',
    })
    await fireEvent.click(screen.getByRole('button', { name: 'Cancel Edit' }))
    expect(handlers.onCancelEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }))
  })

  // The steering precondition lives in the Worker, which reports it per item as
  // `canSteer`. These cases pin that the browser only reads the flag; the
  // precondition itself is covered by the store's own tests.
  it('offers Steer for the head that the Worker marks steerable', async () => {
    const handlers = renderQueue({
      supportsSteering: true,
      items: [item('one', { canSteer: true }), item('two', { canSteer: true })],
    })
    expect(screen.getAllByRole('button', { name: 'Steer' })).toHaveLength(1)
    expect(screen.getByTestId('queued-input-one')).toContainElement(screen.getByRole('button', { name: 'Steer' }))

    await fireEvent.click(screen.getByRole('button', { name: 'Steer' }))

    expect(handlers.onSteer).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }))
  })

  it('does not offer Steer for a head that the Worker refuses to steer', () => {
    renderQueue({ supportsSteering: true, items: [item('one', { canSteer: false })] })
    expect(screen.queryByRole('button', { name: 'Steer' })).not.toBeInTheDocument()
  })

  it('does not offer Steer when the provider does not support steering', () => {
    renderQueue({ supportsSteering: false, items: [item('one', { canSteer: true })] })
    expect(screen.queryByRole('button', { name: 'Steer' })).not.toBeInTheDocument()
  })

  it('offers Retry for a failed head', async () => {
    const handlers = renderQueue({ items: [item('one', { state: AgentInputState.FAILED, error: 'offline' })] })
    expect(screen.getByText('offline')).toBeInTheDocument()
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(handlers.onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), false)
  })

  it('does not offer Retry while the failed head is edited', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.FAILED, editOwnerClientId: 'client-a' })] })
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
  })

  it('marks a delivery-uncertain retry as requiring confirmation', async () => {
    const handlers = renderQueue({ items: [item('one', { state: AgentInputState.DELIVERY_UNCERTAIN })] })
    await fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(handlers.onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), true)
  })

  it('keeps the first Delete click from deleting', async () => {
    const handlers = renderQueue({ items: [item('one')] })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(handlers.onDelete).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()
  })

  it('deletes on the second Delete click', async () => {
    const handlers = renderQueue({ items: [item('one')] })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete?' }))
    expect(handlers.onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }))
  })

  it('arms Delete as an outline, never as a filled danger button', async () => {
    renderQueue({ items: [item('one')] })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const armed = screen.getByRole('button', { name: 'Confirm delete?' })
    // `data-variant` alone paints a filled danger background. Keeping the
    // `outline` class is what reduces it to a danger BORDER, so the pair is
    // the contract, not either half.
    expect(armed).toHaveAttribute('data-variant', 'danger')
    expect(armed.classList.contains('outline')).toBe(true)
  })

  it('disarms Delete when the button loses focus', async () => {
    const handlers = renderQueue({ items: [item('one')] })
    await fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await fireEvent.blur(screen.getByRole('button', { name: 'Confirm delete?' }))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
    expect(handlers.onDelete).not.toHaveBeenCalled()
  })

  it('names every row action although the row renders no button text', () => {
    renderQueue({ items: [item('one'), item('two')], supportsSteering: true })
    const first = screen.getByTestId('queued-input-one')
    for (const name of ['Move Up', 'Move Down', 'Edit', 'Delete']) {
      const button = within(first).getByRole('button', { name })
      expect(button).toBeInTheDocument()
      expect(button.textContent).toBe('')
    }
  })

  it('puts Steer last, as the one action that keeps a visible label', () => {
    renderQueue({
      items: [item('one', { canSteer: true }), item('two')],
      supportsSteering: true,
    })
    const first = screen.getByTestId('queued-input-one')
    const buttons = within(first).getAllByRole('button')
    const steer = within(first).getByRole('button', { name: 'Steer' })
    expect(buttons[buttons.length - 1]).toBe(steer)
    expect(steer).toHaveTextContent('Steer')
  })
})
