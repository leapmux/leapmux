import type { ToolHeaderActionsCallerProps, ToolHeaderActionsLayoutProps } from './messageActions'
import { describe, expect, it, vi } from 'vitest'
import { buildMessageActions, leadingActions, trailingActions } from './messageActions'

function ids(caller?: ToolHeaderActionsCallerProps, layout?: ToolHeaderActionsLayoutProps) {
  return buildMessageActions(caller, layout).map(a => a.id)
}

describe('buildMessageActions', () => {
  it('offers nothing for a row with no actions', () => {
    expect(ids()).toEqual([])
    expect(ids({}, {})).toEqual([])
  })

  it('offers only the JSON copy for a plain message', () => {
    expect(ids({}, { onCopyJson: () => {} })).toEqual(['copy-json'])
  })

  it('adds the quotable pair when the provider extracted text', () => {
    expect(ids(
      { onCopyMarkdown: () => {}, onReply: () => {} },
      { onCopyJson: () => {} },
    )).toEqual(['copy-json', 'copy-markdown', 'quote'])
  })

  it('adds the tool-result actions the provider metadata gates', () => {
    expect(ids(
      { onCopyContent: () => {} },
      { onCopyJson: () => {}, hasDiff: true, onToggleDiffView: () => {}, onToggleExpand: () => {} },
    )).toEqual(['copy-json', 'copy-content', 'diff-view', 'expand'])
  })

  it('withholds the diff toggle when the result has no diff', () => {
    // `hasDiff` and the handler are separate gates, and both must hold.
    expect(ids({}, { onToggleDiffView: () => {} })).toEqual([])
    expect(ids({}, { hasDiff: true })).toEqual([])
  })

  it('labels each copy for its own scope', () => {
    const actions = buildMessageActions(
      { onCopyMarkdown: () => {}, onCopyContent: () => {}, copyContentLabel: 'Copy Command' },
      { onCopyJson: () => {} },
    )
    expect(actions.map(a => a.label)).toEqual(['Copy Raw JSON', 'Copy Markdown', 'Copy Command'])
  })

  it('falls back to a bare Copy when the renderer named no content label', () => {
    const [action] = buildMessageActions({ onCopyContent: () => {} }, {})
    expect(action.label).toBe('Copy')
  })

  it('reports the copied state in the label', () => {
    const actions = buildMessageActions(
      { onCopyMarkdown: () => {}, markdownCopied: true },
      { onCopyJson: () => {}, jsonCopied: true },
    )
    expect(actions.map(a => a.label)).toEqual(['Copied', 'Copied'])
  })

  it('names the toggles by what they will do, not by the current state', () => {
    const unified = buildMessageActions({}, {
      hasDiff: true,
      diffView: 'unified',
      onToggleDiffView: () => {},
      expanded: false,
      onToggleExpand: () => {},
    })
    expect(unified.map(a => a.label)).toEqual(['Switch to split view', 'Expand'])

    const split = buildMessageActions({}, {
      hasDiff: true,
      diffView: 'split',
      onToggleDiffView: () => {},
      expanded: true,
      onToggleExpand: () => {},
    })
    expect(split.map(a => a.label)).toEqual(['Switch to unified view', 'Collapse'])
  })

  it('uses the renderer expand label when the row supplies one', () => {
    const [action] = buildMessageActions({}, { onToggleExpand: () => {}, expandLabel: 'Show 40 more lines' })
    expect(action.label).toBe('Show 40 more lines')
  })

  it('runs the handler the caller supplied', () => {
    const onReply = vi.fn()
    const [action] = buildMessageActions({ onReply }, {})
    action.run()
    expect(onReply).toHaveBeenCalledTimes(1)
  })

  // The expand toggle sits inside a header that is itself click-to-expand.
  it('marks only the expand toggle as needing the event stopped', () => {
    const actions = buildMessageActions(
      { onReply: () => {} },
      { onCopyJson: () => {}, onToggleExpand: () => {} },
    )
    expect(actions.filter(a => a.stopPropagation).map(a => a.id)).toEqual(['expand'])
  })
})

describe('leadingActions', () => {
  const all = () => buildMessageActions(
    { onCopyMarkdown: () => {}, onCopyContent: () => {}, onReply: () => {} },
    { onCopyJson: () => {}, hasDiff: true, onToggleDiffView: () => {}, onToggleExpand: () => {} },
  )

  it('reads copies-then-quote on an ordinary row', () => {
    expect(leadingActions(all(), false).map(a => a.id))
      .toEqual(['copy-json', 'copy-markdown', 'copy-content', 'quote'])
  })

  it('moves quote to second on a mirrored row so it lands nearest the bubble', () => {
    expect(leadingActions(all(), true).map(a => a.id))
      .toEqual(['quote', 'copy-json', 'copy-markdown', 'copy-content'])
  })

  it('skips an action the row does not offer, in both orders', () => {
    const some = buildMessageActions({ onReply: () => {} }, { onCopyJson: () => {} })
    expect(leadingActions(some, false).map(a => a.id)).toEqual(['copy-json', 'quote'])
    expect(leadingActions(some, true).map(a => a.id)).toEqual(['quote', 'copy-json'])
  })

  it('never includes a trailing toggle', () => {
    expect(leadingActions(all(), false).map(a => a.id)).not.toContain('expand')
    expect(leadingActions(all(), false).map(a => a.id)).not.toContain('diff-view')
  })
})

describe('trailingActions', () => {
  it('keeps the view toggles in a fixed order', () => {
    const actions = buildMessageActions({}, {
      hasDiff: true,
      onToggleDiffView: () => {},
      onToggleExpand: () => {},
    })
    expect(trailingActions(actions).map(a => a.id)).toEqual(['diff-view', 'expand'])
  })

  it('is empty when the row has neither toggle', () => {
    expect(trailingActions(buildMessageActions({}, { onCopyJson: () => {} }))).toEqual([])
  })
})
