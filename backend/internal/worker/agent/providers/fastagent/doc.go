// Package fastagent drives fast-agent-mcp over the Agent Client Protocol.
//
// fast-agent speaks standard ACP on `fast-agent acp`, so this package adds
// only what is fast-agent's own: the launch arguments, the session store under
// $FAST_AGENT_HOME, and the child-environment keys the scrub list keeps.
package fastagent
