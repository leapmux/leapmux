import type { LiveCompaction } from '~/stores/chatCompaction'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { CompactionIndicator } from './CompactionIndicator'

describe('CompactionIndicator', () => {
  it('shows the running compaction and removes it when it ends', () => {
    const [value, setValue] = createSignal<LiveCompaction | undefined>({ trigger: 'auto' })
    const view = render(() => <CompactionIndicator compaction={value()} />)
    expect(view.getByTestId('compaction-indicator')).toHaveTextContent('Compacting context... (auto)')
    setValue({})
    expect(view.getByTestId('compaction-indicator')).toHaveTextContent(/^\s*Compacting context\.\.\.$/)
    setValue(undefined)
    expect(view.queryByTestId('compaction-indicator')).toBeNull()
  })

  it('shows no indicator without a compaction', () => {
    const view = render(() => <CompactionIndicator />)
    expect(view.queryByTestId('compaction-indicator')).toBeNull()
  })
})
