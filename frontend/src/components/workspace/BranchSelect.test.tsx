/// <reference types="vitest/globals" />
import type { GitBranchEntry } from '~/generated/leapmux/v1/git_pb'
import { fireEvent, render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { BranchSelect, partitionBranches } from './BranchSelect'

function entry(name: string, isRemote = false): GitBranchEntry {
  return { $typeName: 'leapmux.v1.GitBranchEntry', name, isRemote } as GitBranchEntry
}

describe('partitionBranches', () => {
  it('splits entries into local and remote arrays', () => {
    const { local, remote } = partitionBranches([
      entry('main'),
      entry('origin/main', true),
      entry('feature'),
    ])
    expect(local.map(b => b.name)).toEqual(['main', 'feature'])
    expect(remote.map(b => b.name)).toEqual(['origin/main'])
  })

  it('preserves input order within each bucket', () => {
    // Important: BranchSelect renders in array order, so partition must
    // not reorder. A future "sort alphabetically" change would silently
    // shift the picker's option order.
    const { local, remote } = partitionBranches([
      entry('feature'),
      entry('origin/x', true),
      entry('main'),
      entry('origin/a', true),
    ])
    expect(local.map(b => b.name)).toEqual(['feature', 'main'])
    expect(remote.map(b => b.name)).toEqual(['origin/x', 'origin/a'])
  })

  it('returns empty arrays for an empty input', () => {
    const { local, remote } = partitionBranches([])
    expect(local).toEqual([])
    expect(remote).toEqual([])
  })

  it('handles all-local or all-remote inputs', () => {
    const allLocal = partitionBranches([entry('a'), entry('b')])
    expect(allLocal.local).toHaveLength(2)
    expect(allLocal.remote).toEqual([])
    const allRemote = partitionBranches([entry('origin/a', true), entry('origin/b', true)])
    expect(allRemote.local).toEqual([])
    expect(allRemote.remote).toHaveLength(2)
  })
})

describe('branchSelect', () => {
  it('shows a loading option when loading is true', () => {
    render(() => (
      <BranchSelect value="" onChange={() => {}} local={[]} remote={[]} loading />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.options[0].textContent).toBe('Loading branches...')
    expect(sel.disabled).toBe(true)
  })

  it('shows the empty option when both local and remote are empty and not loading', () => {
    render(() => (
      <BranchSelect value="" onChange={() => {}} local={[]} remote={[]} />
    ))
    expect(screen.getByText('No branches found')).toBeInTheDocument()
  })

  it('renders local and remote in separate optgroups', () => {
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('main'), entry('feature')]}
        remote={[entry('origin/x', true)]}
      />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    const groups = Array.from(sel.querySelectorAll('optgroup')).map(g => g.label)
    expect(groups).toEqual(['Local', 'Remote'])
    const optionTexts = Array.from(sel.options).map(o => o.textContent?.trim())
    expect(optionTexts).toEqual(expect.arrayContaining(['main', 'feature', 'origin/x']))
  })

  it('only renders the Local optgroup when remote is empty', () => {
    render(() => (
      <BranchSelect value="" onChange={() => {}} local={[entry('main')]} remote={[]} />
    ))
    const groups = Array.from(document.querySelectorAll('optgroup')).map(g => g.label)
    expect(groups).toEqual(['Local'])
  })

  it('only renders the Remote optgroup when local is empty', () => {
    render(() => (
      <BranchSelect value="" onChange={() => {}} local={[]} remote={[entry('origin/x', true)]} />
    ))
    const groups = Array.from(document.querySelectorAll('optgroup')).map(g => g.label)
    expect(groups).toEqual(['Remote'])
  })

  it('marks the current branch with " (current)" when showCurrent is true', () => {
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('main'), entry('feature')]}
        remote={[]}
        currentBranch="feature"
        showCurrent
      />
    ))
    expect(screen.getByText(/feature \(current\)/)).toBeInTheDocument()
  })

  it('does not mark anything when showCurrent is false', () => {
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('main')]}
        remote={[]}
        currentBranch="main"
      />
    ))
    expect(screen.queryByText(/\(current\)/)).toBeNull()
  })

  it('renders a prompt option when showPrompt is true', () => {
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('main')]}
        remote={[]}
        showPrompt
      />
    ))
    expect(screen.getByText('Select a branch...')).toBeInTheDocument()
  })

  it('disabled prop disables the select', () => {
    render(() => (
      <BranchSelect value="" onChange={() => {}} local={[entry('main')]} remote={[]} disabled />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    expect(sel.disabled).toBe(true)
  })

  it('fires onChange with the new value when the user picks a branch', () => {
    const onChange = vi.fn()
    render(() => (
      <BranchSelect
        value=""
        onChange={onChange}
        local={[entry('main'), entry('feature')]}
        remote={[]}
      />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    fireEvent.change(sel, { target: { value: 'feature' } })
    expect(onChange).toHaveBeenCalledWith('feature')
  })

  it('renders both shared local and remote refs side by side', () => {
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('shared')]}
        remote={[entry('origin/shared', true)]}
      />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    const values = Array.from(sel.options).map(o => o.value)
    expect(values).toEqual(expect.arrayContaining(['shared', 'origin/shared']))
  })

  it('preserves input order within each optgroup', () => {
    // Catches a future "sort branches alphabetically" change inside the
    // component — the picker must render them in the order the caller
    // supplied (which mirrors the worker's for-each-ref ordering).
    render(() => (
      <BranchSelect
        value=""
        onChange={() => {}}
        local={[entry('feature'), entry('main')]}
        remote={[entry('origin/x', true), entry('origin/a', true)]}
      />
    ))
    const sel = screen.getByRole('combobox') as HTMLSelectElement
    const local = Array.from(sel.querySelectorAll('optgroup[label="Local"] option')).map(o => o.textContent?.trim())
    const remote = Array.from(sel.querySelectorAll('optgroup[label="Remote"] option')).map(o => o.textContent?.trim())
    expect(local).toEqual(['feature', 'main'])
    expect(remote).toEqual(['origin/x', 'origin/a'])
  })
})
