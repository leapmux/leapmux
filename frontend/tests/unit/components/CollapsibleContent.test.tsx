import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { createMessageRenderCacheStore } from '~/components/chat/messageRenderCache'
import { CollapsibleContent } from '~/components/chat/results/CollapsibleContent'
import { toolResultContentAnsi, toolResultContentPre } from '~/components/chat/toolStyles.css'

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

  it('keeps the ANSI render branch when escapes are outside the collapsed display slice', () => {
    const text = 'plain prefix\n\x1B[31mred after fold\x1B[0m'
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text={text} display="plain prefix" isCollapsed={true} />
    ))

    const div = container.firstElementChild as HTMLElement
    expect(div.classList.contains(toolResultContentAnsi)).toBe(true)
    expect(div.classList.contains(toolResultContentPre)).toBe(false)
  })

  it('renders plain pre when ansi-or-pre kind sees no ANSI escapes', () => {
    const { container } = render(() => (
      <CollapsibleContent kind="ansi-or-pre" text="plain" display="plain" isCollapsed={false} />
    ))
    // No ANSI → no styled inner spans, just the plain text.
    expect(container.textContent).toBe('plain')
  })

  it('strips ANSI escape bytes from the plain fallback while syntax work is paused', () => {
    const { container } = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={'\x1B[31mred\x1B[0m'}
        display={'\x1B[31mred\x1B[0m'}
        isCollapsed={false}
        context={{ premeasureMode: true }}
      />
    ))

    expect(container.textContent).toBe('red')
  })

  it('does not reuse visible ANSI HTML from cache during hidden premeasure', () => {
    const text = '\x1B[31mred\x1B[0m'
    const renderCache = createMessageRenderCacheStore().forRow('row-v1')

    const visible = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={text}
        display={text}
        isCollapsed={false}
        context={{ renderCache }}
      />
    ))
    expect(visible.container.firstElementChild?.classList.contains(toolResultContentAnsi)).toBe(true)

    const premeasure = render(() => (
      <CollapsibleContent
        kind="ansi-or-pre"
        text={text}
        display={text}
        isCollapsed={false}
        context={{ renderCache, premeasureMode: true }}
      />
    ))
    const body = premeasure.container.firstElementChild as HTMLElement

    expect(body.classList.contains(toolResultContentPre)).toBe(true)
    expect(body.classList.contains(toolResultContentAnsi)).toBe(false)
    expect(body.querySelector('span')).toBeNull()
    expect(body.textContent).toBe('red')
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
