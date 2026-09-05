import type { MessageInitShape } from '@bufbuild/protobuf'
import { create } from '@bufbuild/protobuf'
import { fireEvent, render, screen } from '@solidjs/testing-library'
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
  activeTurn?: boolean
  activeTurnKind?: AgentInputKind
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
    activeTurn: overrides.activeTurn,
    activeTurnKind: overrides.activeTurn ? (overrides.activeTurnKind ?? AgentInputKind.USER_MESSAGE) : AgentInputKind.UNSPECIFIED,
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

  it('moves inputs with keyboard actions', async () => {
    const handlers = renderQueue()
    const second = screen.getByTestId('queued-input-two')
    await fireEvent.click(second.querySelector('button')!)
    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), 'one')
  })

  it('moves a dragged input before its drop target', async () => {
    const handlers = renderQueue()
    const first = screen.getByTestId('queued-input-one')
    const second = screen.getByTestId('queued-input-two')

    await fireEvent.dragStart(second)
    await fireEvent.drop(first)

    expect(handlers.onMove).toHaveBeenCalledWith(expect.objectContaining({ id: 'two' }), 'one')
  })

  it('does not move an item across a dispatching head', () => {
    renderQueue({ items: [item('one', { state: AgentInputState.DISPATCHING }), item('two')] })
    const second = screen.getByTestId('queued-input-two')
    expect(second.querySelector('button')).toBeDisabled()
  })

  it('shows Take Over for an edit owned by another client', async () => {
    const handlers = renderQueue({ items: [item('one', { editOwnerClientId: 'client-b' })] })
    await fireEvent.click(screen.getByText('Take Over'))
    expect(handlers.onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'one' }), true)
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

  it('offers Steer only for the eligible active queue head', () => {
    renderQueue({ activeTurn: true, supportsSteering: true })
    expect(screen.getAllByText('Steer')).toHaveLength(1)
    expect(screen.getByTestId('queued-input-one')).toContainElement(screen.getByText('Steer'))
  })

  it.each([
    AgentInputKind.AUTO_CONTINUE,
    AgentInputKind.CONTROL_FEEDBACK,
  ])('offers Steer for steerable generated input kind %s', (kind) => {
    renderQueue({
      activeTurn: true,
      supportsSteering: true,
      items: [item('generated', { kind })],
    })
    expect(screen.getByText('Steer')).toBeInTheDocument()
  })

  it('does not offer Steer for an operation or a compaction turn', () => {
    const first = renderQueue({
      activeTurn: true,
      supportsSteering: true,
      items: [item('compact', { kind: AgentInputKind.COMPACT_CONTEXT })],
    })
    expect(screen.queryByText('Steer')).not.toBeInTheDocument()
    first.onSteer.mockClear()
    renderQueue({ activeTurn: true, activeTurnKind: AgentInputKind.COMPACT_CONTEXT, supportsSteering: true })
    expect(screen.queryByText('Steer')).not.toBeInTheDocument()
  })

  it('does not offer Steer for a plan execution input', () => {
    renderQueue({
      activeTurn: true,
      supportsSteering: true,
      items: [item('plan', { kind: AgentInputKind.PLAN_EXECUTION })],
    })
    expect(screen.queryByText('Steer')).not.toBeInTheDocument()
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
