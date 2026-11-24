package dirac

import (
	"encoding/json"
	"fmt"
	"math"
)

type diracTaskletLifecycle struct {
	ID         string
	State      string
	CreatedAt  float64
	Assignment *string
	Raw        json.RawMessage
}

type diracTaskletJournal struct {
	Lifecycle     diracTaskletLifecycle
	Outcome       json.RawMessage
	PriorOutcomes []json.RawMessage
}

func decodeDiracTaskletJournal(raw []byte, taskletID string) (diracTaskletJournal, error) {
	var journal diracTaskletJournal
	if raw == nil {
		return journal, nil
	}
	var events []json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil || events == nil {
		return journal, fmt.Errorf("the Dirac tasklet journal is not an event array")
	}
	seen := make(map[string]struct{}, len(events))
	for _, rawEvent := range events {
		var event struct {
			ID            string   `json:"id"`
			TaskletID     string   `json:"taskletId"`
			CreatedAt     *float64 `json:"createdAt"`
			Communication struct {
				Type       string          `json:"type"`
				State      string          `json:"state"`
				Assignment *string         `json:"assignment"`
				Outcome    json.RawMessage `json:"outcome"`
				Message    json.RawMessage `json:"message"`
				Response   json.RawMessage `json:"response"`
				Request    *struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"request"`
			} `json:"communication"`
		}
		if err := json.Unmarshal(rawEvent, &event); err != nil {
			return journal, err
		}
		if event.ID == "" || event.TaskletID != taskletID || event.CreatedAt == nil || math.IsNaN(*event.CreatedAt) || math.IsInf(*event.CreatedAt, 0) {
			return journal, fmt.Errorf("the Dirac tasklet event has an invalid native owner or time")
		}
		if _, duplicate := seen[event.ID]; duplicate {
			return journal, fmt.Errorf("the Dirac tasklet journal repeats an event identity")
		}
		seen[event.ID] = struct{}{}
		communication := event.Communication
		switch communication.Type {
		case "lifecycle":
			if communication.State != "accepted" && communication.State != "resumed" {
				return journal, fmt.Errorf("the Dirac tasklet lifecycle has an unknown state")
			}
			journal.Lifecycle = diracTaskletLifecycle{ID: event.ID, State: communication.State, CreatedAt: *event.CreatedAt, Assignment: communication.Assignment, Raw: rawEvent}
			if communication.State == "resumed" {
				if len(journal.Outcome) != 0 {
					journal.PriorOutcomes = append(journal.PriorOutcomes, journal.Outcome)
				}
				journal.Outcome = nil
			}
		case "outcome":
			var outcome diracTaskletOutcome
			if err := json.Unmarshal(communication.Outcome, &outcome); err != nil || !outcome.valid() {
				return journal, fmt.Errorf("the Dirac tasklet event has an invalid outcome")
			}
			journal.Outcome = communication.Outcome
		case "message":
			var message struct {
				TaskletID string `json:"taskletId"`
				Type      string `json:"type"`
			}
			if err := json.Unmarshal(communication.Message, &message); err != nil || message.TaskletID != taskletID {
				return journal, fmt.Errorf("the Dirac tasklet message has another owner")
			}
			switch message.Type {
			case "guidance", "question", "reply", "permission_decision":
			default:
				return journal, fmt.Errorf("the Dirac tasklet message has an unknown type")
			}
		case "response":
			if _, err := diracStoredObject(communication.Response); err != nil {
				return journal, err
			}
		case "interaction":
			request := communication.Request
			if request == nil || request.ID == "" || request.Status != "pending" && request.Status != "resolved" && request.Status != "retired" {
				return journal, fmt.Errorf("the Dirac tasklet interaction has invalid request state")
			}
		case "waiting_for_message":
		default:
			return journal, fmt.Errorf("the Dirac tasklet event has an unknown communication type")
		}
	}
	return journal, nil
}
