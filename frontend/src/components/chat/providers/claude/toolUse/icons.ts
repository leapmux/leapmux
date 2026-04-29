import type { LucideIcon } from 'lucide-solid'
import Bot from 'lucide-solid/icons/bot'
import ChevronsRight from 'lucide-solid/icons/chevrons-right'
import ClockFading from 'lucide-solid/icons/clock-fading'
import Eye from 'lucide-solid/icons/eye'
import FilePen from 'lucide-solid/icons/file-pen'
import FilePlus from 'lucide-solid/icons/file-plus'
import FolderSearch from 'lucide-solid/icons/folder-search'
import Globe from 'lucide-solid/icons/globe'
import ListTodo from 'lucide-solid/icons/list-todo'
import OctagonX from 'lucide-solid/icons/octagon-x'
import PlaneTakeoff from 'lucide-solid/icons/plane-takeoff'
import PocketKnife from 'lucide-solid/icons/pocket-knife'
import Search from 'lucide-solid/icons/search'
import Terminal from 'lucide-solid/icons/terminal'
import TextSearch from 'lucide-solid/icons/text-search'
import TicketsPlane from 'lucide-solid/icons/tickets-plane'
import Vote from 'lucide-solid/icons/vote'
import Webhook from 'lucide-solid/icons/webhook'
import Wrench from 'lucide-solid/icons/wrench'
import { CLAUDE_TOOL } from '~/types/toolMessages'
import { isClaudeMcpTool } from '../extractors/mcp'

export function toolIconFor(name: string): LucideIcon {
  switch (name) {
    case CLAUDE_TOOL.BASH: return Terminal
    case CLAUDE_TOOL.READ: return Eye
    case CLAUDE_TOOL.WRITE: return FilePlus
    case CLAUDE_TOOL.EDIT: return FilePen
    case CLAUDE_TOOL.GREP: return TextSearch
    case CLAUDE_TOOL.GLOB: return FolderSearch
    case CLAUDE_TOOL.TASK: return Bot
    case CLAUDE_TOOL.AGENT: return Bot
    case CLAUDE_TOOL.WEB_FETCH: return Globe
    case CLAUDE_TOOL.WEB_SEARCH: return Globe
    case CLAUDE_TOOL.TODO_WRITE: return ListTodo
    case CLAUDE_TOOL.ENTER_PLAN_MODE: return TicketsPlane
    case CLAUDE_TOOL.EXIT_PLAN_MODE: return PlaneTakeoff
    case CLAUDE_TOOL.ASK_USER_QUESTION: return Vote
    case CLAUDE_TOOL.TASK_OUTPUT: return ClockFading
    case CLAUDE_TOOL.SKILL: return PocketKnife
    case CLAUDE_TOOL.TOOL_SEARCH: return Search
    case CLAUDE_TOOL.TASK_STOP: return OctagonX
    case CLAUDE_TOOL.REMOTE_TRIGGER: return Webhook
    default: return isClaudeMcpTool(name) ? Wrench : ChevronsRight
  }
}
