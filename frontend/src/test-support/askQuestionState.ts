import type { AskQuestionState } from '~/components/chat/controls/types'
import { createSignal } from 'solid-js'

export function createAskQuestionState(seed: {
  selections?: Record<number, string[]>
  customTexts?: Record<number, string>
  currentPage?: number
} = {}): AskQuestionState {
  const [selections, setSelections] = createSignal(seed.selections ?? {})
  const [customTexts, setCustomTexts] = createSignal(seed.customTexts ?? {})
  const [currentPage, setCurrentPage] = createSignal(seed.currentPage ?? 0)
  return { selections, setSelections, customTexts, setCustomTexts, currentPage, setCurrentPage }
}
