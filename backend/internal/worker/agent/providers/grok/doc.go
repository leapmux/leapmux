// Package grok implements the Grok Build provider on the ACP base. Grok Build
// runs `grok agent --no-leader stdio` and speaks the Agent Client Protocol with
// its own `_x.ai/` extensions: questions, plan approval, MCP elicitation and
// folder trust as extension requests; subagents in child sessions of their own;
// workflows and goals as session notifications; and turns that the agent
// starts by itself, which only a `turn_completed` notification ends.
package grok
