package agenttest

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// SendChildInput keeps error assertions concise in child transport tests.
func SendChildInput(provider agent.ChildSteerer, key string, input agent.ChildInput) error {
	_, err := provider.SendChildInput(key, input)
	return err
}

// SteerChildInput keeps error assertions concise in child steering tests.
func SteerChildInput(provider agent.ChildSteerer, key, content string, attachments []*leapmuxv1.Attachment) error {
	_, err := provider.SteerChildInput(key, agent.SteerRequest{Content: content, Attachments: attachments})
	return err
}

// SteerChildRequest keeps error assertions concise when a test passes a full SteerRequest.
func SteerChildRequest(provider agent.ChildSteerer, key string, request agent.SteerRequest) error {
	_, err := provider.SteerChildInput(key, request)
	return err
}

// SendChildInputToOwner exercises manager routing before returning its error.
func SendChildInputToOwner(manager *agent.Manager, root, key string, input agent.ChildInput) error {
	_, err := manager.SendChildInput(root, key, input)
	return err
}
