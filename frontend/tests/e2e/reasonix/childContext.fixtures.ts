/** Build the native child-context structure captured from Reasonix 2.53.1. */
export function reasonixChildContextPack(prompt: string): string {
  return `<subagent-context event="SubagentStart">\nBefore acting, check the available skills and tools.\n</subagent-context>\n\n<workspace-context event="SubagentWorkspace">\nCurrent workspace: "/private/project"\n</workspace-context>\n\n${prompt}\n\n<completion-contract>\nCall complete_subtask when you believe this sub-task is done.\n</completion-contract>\n\n<execution-policy preset="balanced" version="3">\nverify=targeted\n</execution-policy>`
}
