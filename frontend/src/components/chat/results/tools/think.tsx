import Brain from 'lucide-solid/icons/brain'
import { proseRenderer, typedRequestLine } from './proseResult'

export const thinkRenderer = proseRenderer<'think'>({
  icon: Brain,
  label: 'Think',
  title(call) {
    return call.title ?? 'Think'
  },
  request(call) {
    if (call.result !== undefined)
      return null
    return typedRequestLine([firstLine(call.request.text)])
  },
})

function firstLine(text: string): string | undefined {
  const line = text.split('\n', 1)[0]?.trim()
  return line || undefined
}
