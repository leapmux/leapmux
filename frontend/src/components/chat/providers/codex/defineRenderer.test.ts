import type { CodexItemType } from '~/types/toolMessages'
import { describe, expect, it } from 'vitest'

import { CODEX_RENDERERS, defineCodexRenderer } from './defineRenderer'

// Tests use synthetic item-type strings (cast through CodexItemType) rather
// than real CODEX_ITEM values to avoid colliding with the production
// renderers that register globally for every CODEX_ITEM value.
const tag = (s: string) => s as CodexItemType

describe('defineCodexRenderer', () => {
  it('returns a wrapper that renders only when item.type matches', () => {
    let calls = 0
    const Renderer = defineCodexRenderer({
      itemTypes: [tag('fooType')],
      render: () => {
        calls++
        return null
      },
    })

    Renderer({ parsed: { item: { type: 'fooType' } } })
    expect(calls).toBe(1)

    // Wrong type — guard returns null without calling render.
    Renderer({ parsed: { item: { type: 'otherType' } } })
    expect(calls).toBe(1)

    // No item — guard returns null.
    Renderer({ parsed: {} })
    expect(calls).toBe(1)
  })

  it('routes when item is unwrapped (top-level type)', () => {
    let calls = 0
    const Renderer = defineCodexRenderer({
      itemTypes: [tag('barType')],
      render: () => {
        calls++
        return null
      },
    })
    Renderer({ parsed: { type: 'barType' } })
    expect(calls).toBe(1)
  })

  it('multi-type spec handles every listed type', () => {
    let calls = 0
    defineCodexRenderer({
      itemTypes: [tag('typeA'), tag('typeB')],
      render: () => {
        calls++
        return null
      },
    })
    expect(CODEX_RENDERERS.has('typeA')).toBe(true)
    expect(CODEX_RENDERERS.has('typeB')).toBe(true)

    CODEX_RENDERERS.get('typeA')!({ item: { type: 'typeA' } })
    CODEX_RENDERERS.get('typeB')!({ item: { type: 'typeB' } })
    expect(calls).toBe(2)
  })

  it('registers the inner render fn (not the wrapper) so the dispatcher can pass already-unwrapped items', () => {
    let received: Record<string, unknown> | undefined
    defineCodexRenderer({
      itemTypes: [tag('captureType')],
      render: (props) => {
        received = props.item
        return null
      },
    })
    const Inner = CODEX_RENDERERS.get('captureType')!
    Inner({ item: { type: 'captureType', extra: 42 } })
    expect(received).toEqual({ type: 'captureType', extra: 42 })
  })
})
