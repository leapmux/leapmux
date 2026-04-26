import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { CollapsibleContent } from '~/components/chat/results/CollapsibleContent'

describe('collapsibleContent', () => {
  it('renders pre body for kind="pre"', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="pre" text="hello\nworld" display="hello\nworld" isCollapsed={false} />
    ))
    expect(container.textContent).toContain('hello')
    expect(container.textContent).toContain('world')
  })

  it('applies the toolResultCollapsed class when isCollapsed is true', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="pre" text="x" display="x" isCollapsed={true} />
    ))
    const div = container.firstElementChild as HTMLElement
    // The collapsed style is composed from a vanilla-extract class; the
    // suffix is appended after the base class. We assert the class list has
    // more than one entry.
    expect(div).not.toBeNull()
    expect(div!.className.split(/\s+/).length).toBeGreaterThan(1)
  })

  it('renders ANSI HTML when ansi-or-pre kind sees ANSI escapes', () => {
    const ansiText = '\x1B[31mred\x1B[0m'
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text={ansiText} display={ansiText} isCollapsed={false} />
    ))
    // renderAnsi converts the ANSI escape into a styled <span>.
    expect(container.querySelector('span')).not.toBeNull()
  })

  it('renders plain pre when ansi-or-pre kind sees no ANSI escapes', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text="plain" display="plain" isCollapsed={false} />
    ))
    // No ANSI → no styled inner spans, just the plain text.
    expect(container.textContent).toBe('plain')
  })

  it('renders markdown body for kind="markdown"', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="markdown" text="# heading" display="# heading" isCollapsed={false} />
    ))
    expect(container.querySelector('h1')).not.toBeNull()
  })

  it('renders markdown-tool-result body using the full text, not display', () => {
    // kind="markdown-tool-result" always renders the full text (display is ignored
    // because markdown can't be cleanly truncated by lines).
    const { container } = render(() => (
      <CollapsibleContent kind="markdown-tool-result" text="**bold**" display="ignored" isCollapsed={false} />
    ))
    expect(container.querySelector('strong')).not.toBeNull()
  })
})
