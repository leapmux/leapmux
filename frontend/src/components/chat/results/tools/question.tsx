import type { QuestionResult } from '../../model/tools/question'
import type { ToolKindRenderer } from './renderer'
import Vote from 'lucide-solid/icons/vote'
import { Show } from 'solid-js'
import { pluralize } from '~/lib/plural'
import { CollapsibleContent } from '../CollapsibleContent'
import { questionBodyMarkdown } from '../questionBody'
import { proseMeta } from './proseResult'

/**
 * The result a question drew, as markdown: one entry for each answer, then the
 * provider's note. A dismissal states only its note, and a result row that drew
 * nothing measured zero height, which held every later row of the transcript hidden.
 */
function resultMarkdown(result: QuestionResult): string {
  return [
    ...result.answers.map(answer => `**${answer.header}** — ${answer.answer ?? '_no answer_'}`),
    ...(result.note ? [result.note] : []),
  ].join('\n\n')
}

export const questionRenderer: ToolKindRenderer<'question'> = {
  icon: Vote,
  label: 'Question',
  title(call) {
    // Several questions state their COUNT, one states its own SENTENCE. Composed here
    // rather than taken from `call.title`, because the request is what every other
    // kind titles itself from -- a provider that stated the count in its own title
    // had it overridden by the first question's twelve-character header.
    //
    // The sentence, never the header: `header` is a short tab caption that tells
    // several questions apart, and preferring it threw away the title the extractor
    // computed for every question that carries one. `QuestionPrompt.question` is required,
    // so this reads the same on every provider.
    const questions = call.request.questions
    if (questions.length > 1)
      return pluralize(questions.length, 'question')
    // `||`, not `??`: `question` is required, so a provider that carries no sentence
    // states an EMPTY one, and `??` would head the row with a blank line that no later
    // step could replace.
    return questions[0]?.question || call.title || 'Question'
  },
  request(call, view) {
    const markdown = questionBodyMarkdown(call.request.questions)
    return <Show when={markdown}>{text => <CollapsibleContent kind="markdown-tool-result" text={text()} isCollapsed={false} {...(view.context !== undefined ? { context: view.context } : {})} />}</Show>
  },
  result(call, view) {
    const text = resultMarkdown(call.result)
    return <Show when={text}>{answers => <CollapsibleContent outputPreview kind="markdown-tool-result" text={answers()} isCollapsed={false} {...(view.context !== undefined ? { context: view.context } : {})} />}</Show>
  },
  resultMeta(call) {
    return proseMeta({ text: resultMarkdown(call.result), format: 'markdown' })
  },
}
