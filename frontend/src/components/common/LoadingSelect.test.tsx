/// <reference types="vitest/globals" />
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { LoadingSelect } from './LoadingSelect'

describe('loadingSelect', () => {
  it('shows the loading sentinel and disables the select while loading', () => {
    render(() => (
      <LoadingSelect
        value=""
        onChange={() => {}}
        loading
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="a">A</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.disabled).toBe(true)
    const labels = Array.from(sel.options).map(o => o.textContent)
    expect(labels).toEqual(['Loading X...'])
  })

  it('shows the empty sentinel and disables the select when empty (and not loading)', () => {
    render(() => (
      <LoadingSelect
        value=""
        onChange={() => {}}
        loading={false}
        isEmpty
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="a">A</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.disabled).toBe(true)
    const labels = Array.from(sel.options).map(o => o.textContent)
    expect(labels).toEqual(['No X'])
  })

  it('renders the children when loaded with non-empty data', () => {
    render(() => (
      <LoadingSelect
        value="b"
        onChange={() => {}}
        loading={false}
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="a">A</option>
        <option value="b">B</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.disabled).toBe(false)
    const labels = Array.from(sel.options).map(o => o.textContent)
    expect(labels).toEqual(['A', 'B'])
    expect(sel.value).toBe('b')
  })

  it('honors the caller-controlled disabled prop even when loaded with data', () => {
    render(() => (
      <LoadingSelect
        value="a"
        onChange={() => {}}
        loading={false}
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
        disabled
      >
        <option value="a">A</option>
      </LoadingSelect>
    ))
    expect((screen.getByRole('combobox') as HTMLSelectElement).disabled).toBe(true)
  })

  it('does NOT render children while loading even if isEmpty=false (Switch gating, not stacking)', () => {
    // Regression guard: if the loading/empty/loaded states were rendered
    // as stacked <Show> blocks instead of a single <Switch>, a re-fetch
    // (loading=true + previous data still in props) would leak the stale
    // options below the loading sentinel.
    render(() => (
      <LoadingSelect
        value=""
        onChange={() => {}}
        loading
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="stale">Stale</option>
      </LoadingSelect>
    ))
    const labels = Array.from((screen.getByRole('combobox') as HTMLSelectElement).options).map(o => o.textContent)
    expect(labels).toEqual(['Loading X...'])
  })

  it('keeps the <select> element mounted (same DOM node) across loading -> loaded transition', () => {
    // The whole reason this component exists: swap option children
    // inside a stable <select> instance so the browser preserves the
    // field's value across async data loads. Wrapping the <select> in
    // a <Show fallback> would unmount and remount the field, resetting
    // selectedIndex back to 0 and disrupting focus.
    const [loading, setLoading] = createSignal(true)
    render(() => (
      <LoadingSelect
        value="a"
        onChange={() => {}}
        loading={loading()}
        isEmpty={false}
        loadingLabel="Loading..."
        emptyLabel="No data"
      >
        <option value="a">A</option>
        <option value="b">B</option>
      </LoadingSelect>
    ))
    const before = screen.getByRole('combobox')
    expect((before as HTMLSelectElement).disabled).toBe(true)
    setLoading(false)
    const after = screen.getByRole('combobox')
    // Same DOM element — only the option children swapped.
    expect(after).toBe(before)
    expect((after as HTMLSelectElement).disabled).toBe(false)
    expect((after as HTMLSelectElement).value).toBe('a')
  })

  it('re-applies props.value to the DOM when the loading -> loaded Switch swap mounts the loaded options', () => {
    // Regression: `<select value={x}>`'s native binding only fires
    // when x changes. When the Switch transitions loading->loaded
    // and swaps the option children, the JSX binding doesn't re-run
    // (props.value is stable), so the browser's selectedIndex is
    // chosen by the new <For> options' DOM order — not by what the
    // caller's form state holds. The dialog then shows e.g. 'main'
    // selected while the underlying gitMode.intent still holds
    // 'feature' and Submit dispatches the wrong target. A
    // createEffect that re-applies props.value after children mount
    // closes the window.
    const [loading, setLoading] = createSignal(true)
    render(() => (
      <LoadingSelect
        value="feature"
        onChange={() => {}}
        loading={loading()}
        isEmpty={false}
        loadingLabel="Loading branches..."
        emptyLabel="No branches"
      >
        <option value="main">main</option>
        <option value="feature">feature</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    // During loading: the only option is the sentinel (value=""),
    // so the browser ignores value='feature' and lands on the
    // sentinel.
    expect(sel.value).toBe('')
    setLoading(false)
    // After loaded options mount, the re-stamp effect runs and the
    // <select> DOM value MUST match the form state, not the first
    // loaded option ('main'). Without the fix, sel.value would be
    // 'main' here.
    expect(sel.value).toBe('feature')
  })

  it('keeps DOM value in sync with props.value updates after the switch transition', () => {
    // Sanity check: the re-stamp effect must not break the normal
    // case where props.value flips while loaded — Solid's native
    // binding handles that path; the effect just runs alongside it.
    const [value, setValue] = createSignal('a')
    render(() => (
      <LoadingSelect
        value={value()}
        onChange={() => {}}
        loading={false}
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="a">A</option>
        <option value="b">B</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.value).toBe('a')
    setValue('b')
    expect(sel.value).toBe('b')
  })

  it('forwards the picked value to onChange', () => {
    const onChange = vi.fn()
    render(() => (
      <LoadingSelect
        value="a"
        onChange={onChange}
        loading={false}
        isEmpty={false}
        loadingLabel="Loading X..."
        emptyLabel="No X"
      >
        <option value="a">A</option>
        <option value="b">B</option>
      </LoadingSelect>
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    fireEvent.change(sel, { target: { value: 'b' } })
    expect(onChange).toHaveBeenCalledWith('b')
  })
})
