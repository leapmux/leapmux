// Package qwen implements the Qwen Code provider on the ACP base. Qwen Code
// runs `qwen --acp` and extends the Agent Client Protocol: it streams a
// foreground subagent in the parent session and tags each update with the tool
// call that spawned it, raises its questions and its plan approval as
// permission requests, pulls steered input between two tool batches, and
// brackets each turn that it starts by itself with its own notifications.
package qwen
