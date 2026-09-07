import type { MessageInitShape } from '@bufbuild/protobuf'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import {
  AgentInputKind,
  AgentInputQueueSnapshotSchema,
  AgentInputState,
  QueuedAgentInputSchema,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { flush } from '~/test-support/async'
import { pointerEvent } from '~/test-support/pointer'
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

function snapshotOf(items: ReturnType<typeof item>[]) {
  return create(AgentInputQueueSnapshotSchema, { agentId: 'agent-1', items })
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
  // A SIGNAL, not a fixed value, because the Worker pushes a whole new snapshot
  // for every queue event -- new objects, new array, same ids. `push` replays
  // that, which is what the keyed-row cases below need.
  const [snapshot, setSnapshot] = createSignal(snapshotOf(overrides.items ?? [item('one'), item('two')]))
  render(() => (
    <AgentInputQueue
      snapshot={snapshot()}
      clientId="client-a"
      activeEditInputId={overrides.activeEditInputId}
      supportsSteering={overrides.supportsSteering ?? false}
      {...handlers}
    />
  ))
  return {
    ...handlers,
    /** Deliver a fresh snapshot, exactly as an `inputQueueChanged` event does. */
    push: (items: ReturnType<typeof item>[]) => setSnapshot(snapshotOf(items)),
  }
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

  it('gives a dispatching row an inert grip, so it offers no drag it cannot do', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    const dispatching = screen.getByTestId('queue-drag-handle-one')
    const movable = screen.getByTestId('queue-drag-handle-two')
    // Hidden rather than REMOVED, so the grip keeps its flex slot and the row's
    // text does not shift one column left. The element being in the document is
    // the half jsdom can check; `visibility: hidden` versus `display: none` is a
    // declaration in `./AgentInputQueue.css.ts`, and vitest loads no stylesheet.
    expect(dispatching).toBeInTheDocument()
    expect(dispatching.className).toContain('dragHandleInert')
    expect(movable.className).not.toContain('dragHandleInert')
  })

  it('withholds the drag activators from a dispatching row grip', async () => {
    const handlers = renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    await flush()
    // An affordance that cannot drag must not behave like one. Hiding the grip
    // is only half of it: without withholding the activators, a press that
    // reached the hidden box would still lift the row.
    screen.getByTestId('queue-drag-handle-one').dispatchEvent(pointerEvent('pointerdown', { x: 10, y: 10, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 10, y: 90, pointerType: 'touch' }))
    document.dispatchEvent(pointerEvent('pointerup', { x: 10, y: 90, pointerType: 'touch' }))
    expect(handlers.onMove).not.toHaveBeenCalled()
    expect(screen.getByTestId('queued-input-one').className).not.toContain('itemDragging')
  })

  it('turns a drop into onMove, which is the wiring the arithmetic tests cannot reach', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two')] })
    await flush()
    // Drives the REAL sensor and the real solid-dnd context, so this covers
    // `onDragEnd`, the `SortableProvider` ids, and the `qi-` prefix on both
    // sides. Every rect is zero in jsdom, so `closestCenter` measures every
    // droppable at distance 0 and keeps the FIRST it saw -- the head. The drop
    // target is therefore deterministic, and the geometry stays with the
    // Playwright specs.
    screen.getByTestId('queued-input-two').dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointerup', { x: 50, y: 90, pointerType: 'mouse' }))

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), 'one')
  })

  it('drops onto the first slot the dispatching head leaves free', async () => {
    const handlers = renderQueue({
      items: [item('one', { state: AgentInputState.DISPATCHING }), item('two'), item('three')],
    })
    await flush()
    // The head is a droppable like any other, so it used to win this collision:
    // the list previewed the move and `handleDragEnd` then discarded it, leaving
    // the row snapped back with no message. The collision detector takes every
    // DISPATCHING row out of the candidates, so the drop lands on the first
    // legal slot instead. Every rect is zero in jsdom, so `closestCenter` keeps
    // the FIRST candidate it saw -- which the filter makes `qi-two`.
    screen.getByTestId('queued-input-three').dispatchEvent(pointerEvent('pointerdown', { x: 50, y: 50, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 70, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointermove', { x: 50, y: 90, pointerType: 'mouse' }))
    document.dispatchEvent(pointerEvent('pointerup', { x: 50, y: 90, pointerType: 'mouse' }))

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'three' }), 'two')
  })

  it('keeps an armed Delete armed across a snapshot the Worker pushes', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two')] })
    await fireEvent.click(within(screen.getByTestId('queued-input-one')).getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('button', { name: 'Confirm delete?' })).toBeInTheDocument()

    // Any queue event at all delivers a whole new snapshot with new objects. A
    // reference-keyed list rebuilt every row here, so the armed state vanished
    // between the user's two clicks and the second click merely re-armed.
    handlers.push([item('one'), item('two', { state: AgentInputState.FAILED, error: 'offline' })])
    await flush()

    const armed = screen.getByRole('button', { name: 'Confirm delete?' })
    expect(armed).toBeInTheDocument()
    await fireEvent.click(armed)
    expect(handlers.onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }))
  })

  it('follows a row into DISPATCHING without remounting it', async () => {
    const handlers = renderQueue({ items: [item('one'), item('two')] })
    await flush()
    const grip = screen.getByTestId('queue-drag-handle-one')
    expect(grip.className).not.toContain('dragHandleInert')

    handlers.push([item('one', { state: AgentInputState.DISPATCHING }), item('two')])
    await flush()

    // The SAME node, because the row's key did not change. Every per-item read
    // in the row therefore has to go through the live accessor; one that
    // captured the item at mount would still report the row as draggable.
    expect(screen.getByTestId('queue-drag-handle-one')).toBe(grip)
    expect(grip.className).toContain('dragHandleInert')
    expect(within(screen.getByTestId('queued-input-two')).getByRole('button', { name: 'Move Up' })).toBeDisabled()
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

  it('gives every row action a name although the row renders no button text', () => {
    renderQueue({ items: [item('one'), item('two')], supportsSteering: true })
    const first = screen.getByTestId('queued-input-one')
    for (const name of ['Move Up', 'Move Down', 'Edit', 'Delete']) {
      // `getByRole` throws when the name matches nothing, so the lookup itself
      // proves the aria-label. The icon needs its own assertion: an EMPTY
      // button has no text content either, so the text check alone passes for a
      // square with nothing drawn in it.
      const button = within(first).getByRole('button', { name })
      expect(button.textContent).toBe('')
      expect(button.querySelector('svg')).not.toBeNull()
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
