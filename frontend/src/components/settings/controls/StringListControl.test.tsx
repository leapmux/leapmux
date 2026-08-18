import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { MAX_STRING_LIST_ITEMS, StringListControl } from './StringListControl'

afterEach(() => {
  cleanup()
})

describe('stringListControl', () => {
  it('shows an empty state when nothing is configured', () => {
    render(() => (
      <StringListControl value={[]} addLabel="Add font" ariaLabel="Fonts" onChange={vi.fn()} />
    ))
    expect(screen.getByText('None configured')).toBeTruthy()
  })

  it('refuses a blank add and does not call onChange', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    const input = screen.getByPlaceholderText('Name') as HTMLInputElement
    fireEvent.input(input, { target: { value: '   ' } })
    expect((screen.getByRole('button', { name: 'Add font' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('removes an item', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter', 'Roboto']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0])
    expect(onChange).toHaveBeenCalledWith(['Roboto'])
  })

  // The hub caps the stored list at usersettings.MaxFonts and refuses a
  // longer one. This tier refuses the same boundary, never a stricter one,
  // so an add the hub would accept is never blocked here.
  it('accepts the entry that reaches the cap and refuses the one past it', () => {
    const atCapMinusOne = Array.from({ length: MAX_STRING_LIST_ITEMS - 1 }, (_, i) => `Face ${i}`)
    const onChange = vi.fn()
    const { unmount } = render(() => (
      <StringListControl value={atCapMinusOne} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.input(screen.getByPlaceholderText('Name'), { target: { value: 'Last' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add font' }))
    expect(onChange).toHaveBeenCalledWith([...atCapMinusOne, 'Last'])
    expect(onChange.mock.calls[0][0]).toHaveLength(MAX_STRING_LIST_ITEMS)
    unmount()

    const atCap = Array.from({ length: MAX_STRING_LIST_ITEMS }, (_, i) => `Face ${i}`)
    const refused = vi.fn()
    render(() => (
      <StringListControl value={atCap} addLabel="Add font" ariaLabel="Fonts" onChange={refused} />
    ))
    fireEvent.input(screen.getByPlaceholderText('Name'), { target: { value: 'OneTooMany' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add font' }))
    expect(refused).not.toHaveBeenCalled()
    expect(screen.getByText(`Too many entries (max ${MAX_STRING_LIST_ITEMS})`)).toBeTruthy()
  })

  it('escape during rename does not commit', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.dblClick(screen.getByText('Inter'))
    const editor = screen.getByLabelText('Rename Fonts') as HTMLInputElement
    fireEvent.input(editor, { target: { value: 'Hack' } })
    fireEvent.keyDown(editor, { key: 'Escape' })
    expect(onChange).not.toHaveBeenCalled()
    expect(screen.getByText('Inter')).toBeTruthy()
  })
})

/**
 * Reordering was bound to `draggable` alone and renaming to a double-click,
 * so the priority order of a font stack -- the whole point of an ordered
 * list -- needed a mouse.
 */
describe('stringListControl keyboard', () => {
  it('enter on a name starts the rename', () => {
    render(() => (
      <StringListControl value={['Inter']} addLabel="Add font" ariaLabel="Fonts" onChange={vi.fn()} />
    ))
    const name = screen.getByRole('button', { name: 'Rename Inter' })
    fireEvent.keyDown(name, { key: 'Enter' })
    expect(screen.getByLabelText('Rename Fonts')).toBeTruthy()
  })

  it('space on a name starts the rename', () => {
    render(() => (
      <StringListControl value={['Inter']} addLabel="Add font" ariaLabel="Fonts" onChange={vi.fn()} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Inter' }), { key: ' ' })
    expect(screen.getByLabelText('Rename Fonts')).toBeTruthy()
  })

  it('alt+ArrowUp moves an entry towards the front', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter', 'Roboto', 'Hack']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Roboto' }), { key: 'ArrowUp', altKey: true })
    expect(onChange).toHaveBeenCalledWith(['Roboto', 'Inter', 'Hack'])
  })

  it('alt+ArrowDown moves an entry towards the back', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter', 'Roboto', 'Hack']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Inter' }), { key: 'ArrowDown', altKey: true })
    expect(onChange).toHaveBeenCalledWith(['Roboto', 'Inter', 'Hack'])
  })

  it('does nothing at either end of the list', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter', 'Roboto']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Inter' }), { key: 'ArrowUp', altKey: true })
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Roboto' }), { key: 'ArrowDown', altKey: true })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('leaves a bare arrow key to the browser', () => {
    const onChange = vi.fn()
    render(() => (
      <StringListControl value={['Inter', 'Roboto']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Roboto' }), { key: 'ArrowUp' })
    expect(onChange).not.toHaveBeenCalled()
  })
})

/**
 * The old FontSettings disabled the add affordance for the whole round trip
 * and said when the write landed. Generalizing it into this control dropped
 * both, so a second name could be queued against a list the hub had not
 * accepted yet.
 */
describe('stringListControl busy state', () => {
  it('disables the add affordance while a write is in flight', async () => {
    const pending = deferred<void>()
    render(() => (
      <StringListControl
        value={['Inter']}
        addLabel="Add font"
        ariaLabel="Fonts"
        onChange={() => pending.promise}
      />
    ))
    const input = screen.getByPlaceholderText('Name') as HTMLInputElement
    const addButton = screen.getByRole('button', { name: 'Add font' }) as HTMLButtonElement

    fireEvent.input(input, { target: { value: 'Hack' } })
    expect(addButton.disabled).toBe(false)
    fireEvent.click(addButton)

    await waitFor(() => expect(input.disabled).toBe(true))
    expect(addButton.disabled).toBe(true)

    pending.resolve()
    await waitFor(() => expect(input.disabled).toBe(false))
  })

  // A write that settles while a LATER one is still in flight must not end
  // the busy state: the add control would re-enable under a list the
  // binding did not accept yet, and a dropped pending list would put the
  // pre-edit prop back on screen under the newer write.
  it('leaves the busy state to the newest write, not the first to settle', async () => {
    const first = deferred<void>()
    const second = deferred<void>()
    let call = 0
    render(() => (
      <StringListControl
        value={['Inter', 'Roboto']}
        addLabel="Add font"
        ariaLabel="Fonts"
        onChange={() => (++call === 1 ? first.promise : second.promise)}
      />
    ))
    const input = screen.getByPlaceholderText('Name') as HTMLInputElement

    // Remove stays live while a write runs, so it is what starts the
    // second one — the add box is disabled by then.
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0]!)
    await waitFor(() => expect(input.disabled).toBe(true))
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0]!)
    expect(call).toBe(2)

    first.resolve()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(input.disabled).toBe(true)
    // The prop still holds both names. Seeing the empty list proves the
    // older reply did not drop the pending one.
    expect(screen.getByText('None configured')).toBeTruthy()

    second.resolve()
    await waitFor(() => expect(input.disabled).toBe(false))
  })

  /**
   * The cap at its boundary, through the ASYNC binding the hub scope uses.
   *
   * The boundary test above writes through a synchronous spy against a fixed
   * prop, so it never meets the pending list at all. Here the entry that
   * REACHES the cap is in flight while the prop still holds the pre-edit
   * list: the rows the control shows are the pending 32, and the add path is
   * closed over the whole round trip -- which is what keeps a second entry
   * from crossing the cap behind a list the hub has not accepted.
   *
   * That closed path is also why the cap's own read of `currentValue()`
   * cannot be reached with a pending list that differs from `props.value`:
   * `pending` is non-null for exactly as long as `busy` is true, and `busy`
   * disables both the name box and the add button. The cap reads the shown
   * list defensively; if the disabled state ever leaves, this boundary needs
   * a test that adds mid-flight.
   */
  it('closes the add path while the entry that reaches the cap is in flight, then refuses the next one', async () => {
    const atCapMinusOne = Array.from({ length: MAX_STRING_LIST_ITEMS - 1 }, (_, i) => `Face ${i}`)
    const [stored, setStored] = createSignal(atCapMinusOne)
    const write = deferred<void>()
    // The hub tier applies the accepted list BEFORE the write resolves
    // (settingsStore merges the reply, then returns), so the control's own
    // bookkeeping settles against a fresh prop.
    const onChange = vi.fn((next: string[]) => write.promise.then(() => setStored(next)))
    render(() => (
      <StringListControl value={stored()} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    const input = screen.getByPlaceholderText('Name') as HTMLInputElement
    const addButton = () => screen.getByRole('button', { name: 'Add font' }) as HTMLButtonElement

    fireEvent.input(input, { target: { value: 'Last' } })
    fireEvent.click(addButton())
    expect(onChange).toHaveBeenCalledWith([...atCapMinusOne, 'Last'])

    await waitFor(() => expect(input.disabled).toBe(true))
    expect(addButton().disabled).toBe(true)
    expect(screen.getAllByRole('button', { name: /^Rename / })).toHaveLength(MAX_STRING_LIST_ITEMS)

    write.resolve()
    await waitFor(() => expect(input.disabled).toBe(false))

    fireEvent.input(input, { target: { value: 'OneTooMany' } })
    fireEvent.click(addButton())
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(screen.getByText(`Too many entries (max ${MAX_STRING_LIST_ITEMS})`)).toBeTruthy()
    expect(screen.getAllByRole('button', { name: /^Rename / })).toHaveLength(MAX_STRING_LIST_ITEMS)
  })

  // Both handlers recompute the whole list, so they must recompute it from
  // the SAME list -- the one the control shows. A hub-scope binding applies
  // nothing until the RPC returns, so `value` still holds the pre-edit list
  // while the write is in flight.
  it('computes the next edit from the list it is showing, not the stale prop', async () => {
    const onChange = vi.fn(() => deferred<void>().promise)
    render(() => (
      <StringListControl value={['Inter', 'Roboto', 'Hack']} addLabel="Add font" ariaLabel="Fonts" onChange={onChange} />
    ))
    fireEvent.keyDown(screen.getByRole('button', { name: 'Rename Hack' }), { key: 'ArrowUp', altKey: true })
    expect(onChange).toHaveBeenLastCalledWith(['Inter', 'Hack', 'Roboto'])

    await waitFor(() => expect(screen.getAllByRole('button', { name: /^Rename / })
      .map(el => el.textContent)).toEqual(['Inter', 'Hack', 'Roboto']))

    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[2]!)
    expect(onChange).toHaveBeenLastCalledWith(['Inter', 'Hack'])
  })
})
