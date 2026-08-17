import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SecretControl } from './SecretControl'

afterEach(() => {
  cleanup()
})

describe('secretControl', () => {
  it('does not submit whitespace-only drafts', () => {
    const onSet = vi.fn(async () => {})
    render(() => <SecretControl isSet={false} ariaLabel="SMTP password" onSet={onSet} />)
    const input = screen.getByLabelText('SMTP password') as HTMLInputElement
    fireEvent.input(input, { target: { value: '   ' } })
    expect((screen.getByRole('button', { name: 'Set' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Set' }))
    expect(onSet).not.toHaveBeenCalled()
  })

  it('enter commits a non-empty draft and clears it on success', async () => {
    const onSet = vi.fn(async () => {})
    render(() => <SecretControl isSet={false} ariaLabel="SMTP password" onSet={onSet} />)
    const input = screen.getByLabelText('SMTP password') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'hunter2' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(onSet).toHaveBeenCalledWith('hunter2'))
    await waitFor(() => expect(input.value).toBe(''))
  })

  // A stored secret can never be redisplayed, so a silently altered
  // credential has no symptom other than a later authentication failure.
  // The trim belongs to the emptiness guard, not to the wire value.
  it('transmits leading and trailing spaces unchanged', async () => {
    const onSet = vi.fn(async () => {})
    render(() => <SecretControl isSet={false} ariaLabel="SMTP password" onSet={onSet} />)
    const input = screen.getByLabelText('SMTP password') as HTMLInputElement
    fireEvent.input(input, { target: { value: '  hunter2  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set' }))
    await waitFor(() => expect(onSet).toHaveBeenCalledWith('  hunter2  '))
  })

  it('shows the rejection message and keeps the draft', async () => {
    const onSet = vi.fn(async () => {
      throw new Error('write refused')
    })
    render(() => <SecretControl isSet={true} ariaLabel="SMTP password" onSet={onSet} />)
    expect(screen.getByRole('button', { name: 'Replace' })).toBeTruthy()
    const input = screen.getByLabelText('SMTP password') as HTMLInputElement
    fireEvent.input(input, { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Replace' }))
    await waitFor(() => expect(screen.getByText('write refused')).toBeTruthy())
    expect(input.value).toBe('hunter2')
  })
})
