package agenttest

import "github.com/leapmux/leapmux/internal/worker/agent"

// SendChildInput keeps error assertions concise in child transport tests.
func SendChildInput(provider agent.ChildSteerer, key string, input agent.ChildInput) error {
	_, err := provider.SendChildInput(key, input)
	return err
}

// SendChildInputToOwner exercises manager routing before returning its error.
func SendChildInputToOwner(manager *agent.Manager, root, key string, input agent.ChildInput) error {
	_, err := manager.SendChildInput(root, key, input)
	return err
}
