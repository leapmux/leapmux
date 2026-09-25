import { render } from '@solidjs/testing-library'
import { For } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentProviderIcon, agentProviderLabel } from './AgentProviderIcon'

// The fallback for a provider with no mark of its own is lucide's Bot icon, and
// lucide stamps its name on the root element.
const FALLBACK_CLASS = 'lucide-bot'

describe('AgentProviderIcon', () => {
  it.each(ALL_PROVIDERS.map(provider => [agentProviderLabel(provider), provider] as const))(
    'renders a mark of its own for %s',
    (_label, provider) => {
      const { container } = render(() => <AgentProviderIcon provider={provider} size={20} />)
      const svg = container.querySelector('svg')
      expect(svg).not.toBeNull()
      expect(svg).not.toHaveClass(FALLBACK_CLASS)
      expect(svg!.getAttribute('width')).toBe('20')
      expect(svg!.getAttribute('height')).toBe('20')
      // A square viewBox keeps every mark on the same footprint, which is what
      // the per-mark padding tunes against.
      const [, , width, height] = svg!.getAttribute('viewBox')!.split(/\s+/).map(Number)
      expect(width).toBeGreaterThan(0)
      expect(width).toBe(height)
    },
  )

  it('renders a different mark for each provider', () => {
    // Each case above proves only that a provider draws SOME mark. A copied `Match`
    // that drew one provider's mark for another would pass there and fail here.
    const marks = ALL_PROVIDERS.map((provider) => {
      const { container, unmount } = render(() => <AgentProviderIcon provider={provider} size={20} />)
      // A gradient id differs on each render, so it cannot tell two marks apart.
      const mark = container.innerHTML.replaceAll(/(id="|url\(#)[^")]+/g, '$1')
      unmount()
      return [agentProviderLabel(provider), mark] as const
    })
    const byMark = new Map<string, string[]>()
    for (const [label, mark] of marks)
      byMark.set(mark, [...(byMark.get(mark) ?? []), label])
    expect([...byMark.values()].filter(labels => labels.length > 1)).toEqual([])
  })

  it.each(ALL_PROVIDERS.map(provider => [agentProviderLabel(provider), provider] as const))(
    'passes the class to the mark for %s',
    (_label, provider) => {
      const { container } = render(() => <AgentProviderIcon provider={provider} size={20} class="probe-class" />)
      expect(container.querySelector('svg')).toHaveClass('probe-class')
    },
  )

  it('passes the class to the fallback', () => {
    const { container } = render(() => <AgentProviderIcon size={20} class="probe-class" />)
    const svg = container.querySelector('svg')
    expect(svg).toHaveClass(FALLBACK_CLASS)
    expect(svg).toHaveClass('probe-class')
  })

  it('renders the fallback for an unspecified provider', () => {
    const { container } = render(() => <AgentProviderIcon provider={AgentProvider.UNSPECIFIED} size={20} />)
    expect(container.querySelector('svg')).toHaveClass(FALLBACK_CLASS)
  })

  it('renders the fallback for an absent provider', () => {
    const { container } = render(() => <AgentProviderIcon size={20} />)
    expect(container.querySelector('svg')).toHaveClass(FALLBACK_CLASS)
  })

  it('gives each gradient mark on one page its own gradient id', () => {
    // Two copies of a gradient mark in one document must not share an id: the
    // second `url(#id)` would then paint with the first copy's gradient, and a
    // copy inside a hidden subtree would paint nothing at all.
    const gradientProviders = [
      AgentProvider.CODEX,
      AgentProvider.CODEWHALE,
      AgentProvider.QWEN_CODE,
      AgentProvider.OH_MY_PI,
    ]
    const { container } = render(() => (
      <>
        <For each={gradientProviders}>{provider => <AgentProviderIcon provider={provider} size={16} />}</For>
        <For each={gradientProviders}>{provider => <AgentProviderIcon provider={provider} size={16} />}</For>
      </>
    ))
    const ids = [...container.querySelectorAll('linearGradient')].map(gradient => gradient.id)
    expect(ids).toHaveLength(gradientProviders.length * 2)
    expect(new Set(ids).size).toBe(ids.length)
    for (const path of container.querySelectorAll('path[fill^="url("]')) {
      const id = /^url\(#(.+)\)$/.exec(path.getAttribute('fill')!)?.[1]
      expect(ids).toContain(id)
    }
  })
})

describe('agentProviderLabel', () => {
  it.each([
    [AgentProvider.CODEWHALE, 'Codewhale'],
    [AgentProvider.KIMI_CODE, 'Kimi Code'],
    [AgentProvider.MIMO_CODE, 'MiMo Code'],
    [AgentProvider.QWEN_CODE, 'Qwen Code'],
    [AgentProvider.OH_MY_PI, 'Oh My Pi'],
    [AgentProvider.GROK_BUILD, 'Grok Build'],
    [AgentProvider.KIRO, 'Kiro'],
    [AgentProvider.AMP, 'Amp'],
    [AgentProvider.CLINE, 'Cline'],
  ])('labels provider %i as %s', (provider, label) => {
    expect(agentProviderLabel(provider)).toBe(label)
  })

  it('labels an unspecified provider as unknown', () => {
    expect(agentProviderLabel(AgentProvider.UNSPECIFIED)).toBe('Unknown')
  })

  it('labels an absent provider as unknown', () => {
    expect(agentProviderLabel()).toBe('Unknown')
  })
})
