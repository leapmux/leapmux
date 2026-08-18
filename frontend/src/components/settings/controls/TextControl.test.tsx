import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred, flush } from '~/test-support/async'
import { TextControl } from './TextControl'

afterEach(() => {
  cleanup()
})

function textbox(name = 'Public URL'): HTMLInputElement {
  return screen.getByRole('textbox', { name }) as HTMLInputElement
}

describe('textControl', () => {
  // One commit per write RPC. A commit on `input` stored each prefix of what
  // the user typed, so `https://h`, `https://hu`, ... each reached the hub in
  // turn, and mail sent inside that window carried the half-typed URL.
  it('commits on change alone, never on a keystroke', () => {
    const onChange = vi.fn()
    render(() => <TextControl value="https://hub.example.com" ariaLabel="Public URL" onChange={onChange} />)
    const input = textbox()

    fireEvent.input(input, { target: { value: 'https://hub.example.co' } })
    fireEvent.input(input, { target: { value: 'https://hub.example.com/x' } })
    expect(onChange).not.toHaveBeenCalled()

    fireEvent.change(input, { target: { value: 'https://hub.example.com/x' } })
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith('https://hub.example.com/x')
  })

  /**
   * The repair reads the binding when the write ANSWERS, not when it is
   * issued.
   *
   * A refusal can arrive long after the commit, and the binding can move in
   * that window — another admin writes the same key, or the store refreshes.
   * A version that captured `props.value` before it issued the write passes
   * every test whose binding holds one value for the whole test; it puts a
   * value on screen here that no tier holds any more, and Solid never
   * corrects it, because the tracked expression did not change.
   */
  it('restores the value the binding holds at reply time, not at issue time', async () => {
    const refused = deferred<boolean>()
    const [stored, setStored] = createSignal<string | undefined>('https://old.example.com')
    render(() => <TextControl value={stored()} ariaLabel="Public URL" onChange={() => refused.promise} />)
    const input = textbox()

    fireEvent.change(input, { target: { value: 'not-a-url' } })
    setStored('https://new.example.com')
    expect(input.value, 'the binding moved while the write was in flight').toBe('https://new.example.com')

    refused.resolve(false)
    await flush()
    expect(input.value).toBe('https://new.example.com')
  })

  it('clears the field when the write is refused and the binding holds nothing', async () => {
    const refused = deferred<boolean>()
    render(() => <TextControl value={undefined} ariaLabel="Public URL" onChange={() => refused.promise} />)
    const input = textbox()

    fireEvent.change(input, { target: { value: 'not-a-url' } })
    refused.resolve(false)
    await flush()
    expect(input.value).toBe('')
  })

  it('keeps the typed text when the write is accepted', async () => {
    const accepted = deferred<boolean>()
    render(() => <TextControl value="https://hub.example.com" ariaLabel="Public URL" onChange={() => accepted.promise} />)
    const input = textbox()

    fireEvent.change(input, { target: { value: 'https://other.example.com' } })
    accepted.resolve(true)
    await flush()
    expect(input.value).toBe('https://other.example.com')
  })

  // Only `false` means refused. A caller with nothing to report returns void,
  // and treating that as a refusal would wipe every accepted edit of a
  // binding that answers synchronously.
  it('leaves the typed text alone when the write reports nothing', async () => {
    render(() => (
      <TextControl
        value="https://hub.example.com"
        ariaLabel="Public URL"
        onChange={() => {}}
      />
    ))
    const input = textbox()

    fireEvent.change(input, { target: { value: 'https://other.example.com' } })
    await flush()
    expect(input.value).toBe('https://other.example.com')
  })
})
