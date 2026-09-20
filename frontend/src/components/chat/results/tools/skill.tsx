import PocketKnife from 'lucide-solid/icons/pocket-knife'
import { proseRenderer } from './proseResult'

export const skillRenderer = proseRenderer<'skill'>({
  icon: PocketKnife,
  label: 'Skill',
  title(call) {
    return call.request.name ? `Skill: /${call.request.name}` : call.title ?? 'Skill'
  },
})
