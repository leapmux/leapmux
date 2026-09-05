import type { Component } from 'solid-js'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import Braces from 'lucide-solid/icons/braces'
import Check from 'lucide-solid/icons/check'
import Columns2 from 'lucide-solid/icons/columns-2'
import Copy from 'lucide-solid/icons/copy'
import FoldVertical from 'lucide-solid/icons/fold-vertical'
import Quote from 'lucide-solid/icons/quote'
import Rows2 from 'lucide-solid/icons/rows-2'
import UnfoldVertical from 'lucide-solid/icons/unfold-vertical'

/**
 * Caller-controlled buttons for a message row's actions. These are the subset
 * whose source is the *renderer* (e.g. an Edit tool's "Copy diff", a markdown
 * tool's "Copy markdown", a reply quote callback).
 *
 * `ToolUseLayout` re-exposes this bag verbatim as `headerActions=`.
 */
export interface ToolHeaderActionsCallerProps {
  onCopyContent?: () => void
  contentCopied?: boolean
  copyContentLabel?: string
  onReply?: () => void
  onCopyMarkdown?: () => void
  markdownCopied?: boolean
}

/**
 * Layout-controlled state for a message row's actions. These come from the
 * wrapping layout/bubble (timestamp, expand state, JSON copy, diff-view toggle),
 * not the per-renderer caller.
 */
export interface ToolHeaderActionsLayoutProps {
  createdAt?: string
  expanded?: boolean
  onToggleExpand?: () => void
  expandLabel?: string
  onCopyJson?: () => void
  jsonCopied?: boolean
  hasDiff?: boolean
  diffView?: DiffViewPreference
  onToggleDiffView?: () => void
  /**
   * The row mirrors its toolbar beside a right-aligned bubble (a user message --
   * see isMirroredMessageRow). Such a row reverses the button order so Quote
   * still lands nearest the bubble; every other row leads with the timestamp.
   */
  mirrored?: boolean
}

export type MessageActionId
  = | 'copy-json'
    | 'copy-markdown'
    | 'copy-content'
    | 'quote'
    | 'diff-view'
    | 'expand'

export interface MessageAction {
  id: MessageActionId
  /** The tooltip on the toolbar button, and the item text in the menu. */
  label: string
  icon: Component<{ size?: number | string, class?: string }>
  /** data-testid for the toolbar button. The menu derives its own from `id`. */
  testId?: string
  /**
   * Destructive. The menu pins these to its foot behind a rule instead of letting
   * its reversal float them under the cursor; see `dangerActions` in
   * ~/components/chat/MessageContextMenuHost.tsx.
   */
  danger?: boolean
  /**
   * Stop the event before running. The expand toggle sits inside a tool header
   * that is itself click-to-expand, so its own click must not also reach it.
   */
  stopPropagation?: boolean
  run: () => void
}

/**
 * Every action a message row offers, in one canonical order.
 *
 * One list with two presentations: `ToolHeaderActions` renders it as the
 * hover-revealed button strip, and `MessageContextMenu` renders it as menu items.
 * The list is what stops them drifting -- and it is why the diff-view and expand
 * toggles stop being mouse-only, which is the same gap this whole feature exists
 * to close.
 *
 * The order is broadest copy to narrowest (whole envelope, rendered markdown, tool
 * content), then Quote, then the view toggles. The toolbar reorders it for a
 * mirrored row; see the grid note there.
 *
 * Provider-specific extraction stays where it belongs: `onCopyMarkdown`/`onReply`
 * come from `plugin.extractQuotableText` and `onCopyContent` from
 * `plugin.toolResultMeta().copyableContent`, both resolved by `MessageBubble`
 * before they reach this function. Nothing here parses a provider's shapes.
 */
export function buildMessageActions(
  caller: ToolHeaderActionsCallerProps | undefined,
  layout: ToolHeaderActionsLayoutProps | undefined,
): MessageAction[] {
  const actions: MessageAction[] = []

  if (layout?.onCopyJson) {
    actions.push({
      id: 'copy-json',
      label: layout.jsonCopied ? 'Copied' : 'Copy Raw JSON',
      icon: layout.jsonCopied ? Check : Braces,
      testId: 'message-copy-json',
      run: layout.onCopyJson,
    })
  }

  if (caller?.onCopyMarkdown) {
    actions.push({
      id: 'copy-markdown',
      label: caller.markdownCopied ? 'Copied' : 'Copy Markdown',
      icon: caller.markdownCopied ? Check : Copy,
      testId: 'message-copy-markdown',
      run: caller.onCopyMarkdown,
    })
  }

  if (caller?.onCopyContent) {
    actions.push({
      id: 'copy-content',
      label: caller.contentCopied ? 'Copied' : (caller.copyContentLabel || 'Copy'),
      icon: caller.contentCopied ? Check : Copy,
      run: caller.onCopyContent,
    })
  }

  if (caller?.onReply) {
    actions.push({
      id: 'quote',
      label: 'Quote',
      icon: Quote,
      testId: 'message-quote',
      run: caller.onReply,
    })
  }

  if (layout?.hasDiff && layout.onToggleDiffView) {
    const unified = layout.diffView === 'unified'
    actions.push({
      id: 'diff-view',
      label: unified ? 'Switch to split view' : 'Switch to unified view',
      icon: unified ? Columns2 : Rows2,
      run: layout.onToggleDiffView,
    })
  }

  if (layout?.onToggleExpand) {
    actions.push({
      id: 'expand',
      label: layout.expanded ? 'Collapse' : (layout.expandLabel || 'Expand'),
      icon: layout.expanded ? FoldVertical : UnfoldVertical,
      stopPropagation: true,
      run: layout.onToggleExpand,
    })
  }

  return actions
}

/**
 * The action ids of the toolbar's leading group, in the order they appear ON
 * SCREEN, for the two row flavors.
 *
 * A mirrored row moves Quote only, from last to second, so it lands nearest the
 * bubble on its right. Everything else keeps its place. The trailing view toggles
 * (`diff-view`, `expand`) sit outside this group and never move.
 */
const SCREEN_ORDER: Record<'mirrored' | 'normal', MessageActionId[]> = {
  normal: ['copy-json', 'copy-markdown', 'copy-content', 'quote'],
  mirrored: ['quote', 'copy-json', 'copy-markdown', 'copy-content'],
}

/** The ids the trailing group renders, always after the leading one. */
const TRAILING_ACTION_IDS: MessageActionId[] = ['diff-view', 'expand']

/**
 * The leading group's actions for a row, ordered as they should READ ON SCREEN --
 * the timestamp notionally first, then these.
 *
 * The caller is responsible for turning this into source order, which for a
 * mirrored row is not the same thing; see `ToolHeaderActions`.
 */
export function leadingActions(actions: MessageAction[], mirrored: boolean): MessageAction[] {
  const order = SCREEN_ORDER[mirrored ? 'mirrored' : 'normal']
  return order
    .map(id => actions.find(a => a.id === id))
    .filter((a): a is MessageAction => a !== undefined)
}

/** The trailing view toggles for a row, in order. */
export function trailingActions(actions: MessageAction[]): MessageAction[] {
  return TRAILING_ACTION_IDS
    .map(id => actions.find(a => a.id === id))
    .filter((a): a is MessageAction => a !== undefined)
}
