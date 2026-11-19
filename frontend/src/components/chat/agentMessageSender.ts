import type { AgentMessageSender } from './model/row'
import { agentProviderLabel } from '~/components/common/AgentProviderIcon'

/** Looks up the title of a tab that the browser holds, by its id. */
export type TabTitleLookup = (tabId: string) => string | undefined

/**
 * The words that state the sender of a message from another agent, in the
 * transcript and in the input queue alike: the title of the sending agent's tab,
 * or its tab id when the browser holds no title for it, and the name of its
 * provider.
 *
 * The browser resolves the title when it draws the words, so a tab that the user
 * renamed after the send shows its new title.
 */
export function agentMessageSenderLabel(sender: AgentMessageSender, tabTitle: TabTitleLookup | undefined): string {
  const title = tabTitle?.(sender.tabId)?.trim() || sender.tabId
  return `Message from ${title} (${agentProviderLabel(sender.provider)})`
}
