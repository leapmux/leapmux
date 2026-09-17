import Users from 'lucide-solid/icons/users'
import { proseRenderer } from './proseResult'

export const agentsRenderer = proseRenderer<'agents'>({
  icon: Users,
  label: 'Agents',
  title(call) {
    return call.request.team?.name ?? call.title ?? 'Agents'
  },
})
