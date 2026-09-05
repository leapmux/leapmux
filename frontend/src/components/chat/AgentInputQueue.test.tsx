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
import { AgentInputQueue } from './AgentInputQueue'

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

  it('moves an input dragged upward before its drop target', async () => {
    const handlers = renderQueue()
    const first = screen.getByTestId('queued-input-one')
    const second = screen.getByTestId('queued-input-two')

    await fireEvent.dragStart(second)
    await fireEvent.drop(first)

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), 'one')
  })

  it('moves an input dragged downward onto the drop target slot', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two'), item('three')] })

    await fireEvent.dragStart(screen.getByTestId('queued-input-one'))
    await fireEvent.drop(screen.getByTestId('queued-input-two'))

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), 'three')
  })

  it('moves an input dragged onto the last row to the end', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two'), item('three')] })

    await fireEvent.dragStart(screen.getByTestId('queued-input-one'))
    await fireEvent.drop(screen.getByTestId('queued-input-three'))

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), '')
  })

  it('ignores a drop on the dragged row itself', async () => {
    const handlers = renderQueue()
    const first = screen.getByTestId('queued-input-one')

    await fireEvent.dragStart(first)
    await fireEvent.drop(first)

    expect(handlers.onMove).not.toHaveBeenCalled()
  })

  it('ignores a drop on a dispatching row', async () => {
    const handlers = renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })

    await fireEvent.dragStart(screen.getByTestId('queued-input-two'))
    await fireEvent.drop(screen.getByTestId('queued-input-one'))

    expect(handlers.onMove).not.toHaveBeenCalled()
  })

  it('does not move an item across a dispatching head', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    const second = screen.getByTestId('queued-input-two')
    expect(within(second).getByRole('button', { name: 'Move Up' })).toBeDisabled()
  })

  it('shows Take Over for an edit owned by another client', async () => {
    const handlers = renderQueue({ items: [item('one', { editOwnerClientId: 'client-b' })] })
    await fireEvent.click(screen.getByText('Take Over'))
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
    await fireEvent.click(screen.getByText('Resume Edit'))
    expect(handlers.onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), false)
  })

  it('cancels the owned edit that this panel loaded', async () => {
    const handlers = renderQueue({
      items: [item('one', { editOwnerClientId: 'client-a' })],
      activeEditInputId: 'one',
    })
    await fireEvent.click(screen.getByText('Cancel Edit'))
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
    await fireEvent.click(screen.getByText('Retry'))
    expect(handlers.onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), false)
  })

  it('does not offer Retry while the failed head is edited', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.FAILED, editOwnerClientId: 'client-a' })] })
    expect(screen.queryByText('Retry')).not.toBeInTheDocument()
  })

  it('marks a delivery-uncertain retry as requiring confirmation', async () => {
    const handlers = renderQueue({ items: [item('one', { state: AgentInputState.DELIVERY_UNCERTAIN })] })
    await fireEvent.click(screen.getByText('Retry'))
    expect(handlers.onRetry).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), true)
  })
})
