package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

const (
	cascadeStatusIdle        = "CASCADE_RUN_STATUS_IDLE"
	cascadeStatusRunning     = "CASCADE_RUN_STATUS_RUNNING"
	cascadeStatusCanceling   = "CASCADE_RUN_STATUS_CANCELING"
	cascadeStatusBusy        = "CASCADE_RUN_STATUS_BUSY"
	antigravityQueueInputTag = "leapmux:queue-input"
	pastEveryStep            = 1 << 30
)

type agentStateEnvelope struct {
	Update json.RawMessage `json:"update"`
}

type agentStateUpdate struct {
	Status          string          `json:"status"`
	FullyIdle       bool            `json:"fullyIdle"`
	TrajectoryID    string          `json:"trajectoryId"`
	MainTrajectory  trajectoryPatch `json:"mainTrajectoryUpdate"`
	Subtrajectories map[string]struct {
		Status   string `json:"status"`
		Metadata struct {
			SubagentSpec struct {
				Role string `json:"role"`
			} `json:"subagentSpec"`
		} `json:"metadata"`
	} `json:"subtrajectoryUpdates"`
	ExecutionStatus struct {
		ModelRequest *modelRequestStatus `json:"modelRequest"`
	} `json:"executionStatus"`
	ContextWindowUsage *contextWindowUsage   `json:"contextWindowUsage"`
	BackgroundTasks    *backgroundTasksPatch `json:"backgroundTasksUpdate"`
}

type trajectoryPatch struct {
	StepsUpdate stepsPatch `json:"stepsUpdate"`
}

type stepsPatch struct {
	Indices     []int             `json:"indices"`
	Steps       []json.RawMessage `json:"steps"`
	TotalLength int               `json:"totalLength"`
}

type modelRequestStatus struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	RetryAt     string `json:"retryAt"`
}

type contextWindowUsage struct {
	EstimatedTokensUsed int64 `json:"estimatedTokensUsed"`
	MaxContextTokens    int64 `json:"maxContextTokens"`
}

type backgroundTasksPatch struct {
	Indices         []int                    `json:"indices"`
	BackgroundTasks []backgroundTaskSnapshot `json:"backgroundTasks"`
	TotalLength     int                      `json:"totalLength"`
}

type backgroundTaskSnapshot struct {
	TaskSnapshot struct {
		TaskID      string `json:"taskId"`
		ToolName    string `json:"toolName"`
		ToolSummary string `json:"toolSummary"`
		Description string `json:"description"`
		StepIndex   int    `json:"stepIndex"`
		LogURI      string `json:"logUri"`
	} `json:"taskSnapshot"`
}

type antigravityErrorDetails struct {
	UserErrorMessage  string `json:"userErrorMessage"`
	ModelErrorMessage string `json:"modelErrorMessage"`
	ShortError        string `json:"shortError"`
	FullError         string `json:"fullError"`
	Details           string `json:"details"`
}

type antigravityStep struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Metadata struct {
		ToolCall struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ArgumentsJSON string `json:"argumentsJson"`
		} `json:"toolCall"`
		ModelUsage map[string]string `json:"modelUsage"`
	} `json:"metadata"`
	RequestedInteraction json.RawMessage `json:"requestedInteraction"`
	PlannerResponse      struct {
		Response         string `json:"response"`
		ModifiedResponse string `json:"modifiedResponse"`
		Thinking         string `json:"thinking"`
	} `json:"plannerResponse"`
	UserInput struct {
		Tags []string `json:"tags"`
	} `json:"userInput"`
	SystemMessage struct {
		AgentMessage *struct {
			Content      string `json:"content"`
			HideFromUser bool   `json:"hideFromUser"`
		} `json:"agentMessage"`
	} `json:"systemMessage"`
	RunCommand struct {
		CommandLine    string `json:"commandLine"`
		CWD            string `json:"cwd"`
		ExitCode       int    `json:"exitCode"`
		CombinedOutput struct {
			Full string `json:"full"`
		} `json:"combinedOutput"`
	} `json:"runCommand"`
	InvokeSubagent struct {
		Subagents []struct {
			TypeName string `json:"typeName"`
			Role     string `json:"role"`
		} `json:"subagents"`
		Results []struct {
			ConversationID string `json:"conversationId"`
		} `json:"results"`
	} `json:"invokeSubagent"`
	Error        antigravityErrorDetails `json:"error"`
	ErrorMessage struct {
		Error antigravityErrorDetails `json:"error"`
	} `json:"errorMessage"`
}

type requestedInteraction struct {
	Permission  *permissionInteraction  `json:"permission"`
	AskQuestion *askQuestionInteraction `json:"askQuestion"`
}

type permissionInteraction struct {
	Resource struct {
		Action string `json:"action"`
		Target string `json:"target"`
	} `json:"resource"`
	ActionDescription       string `json:"actionDescription"`
	SuggestedPersistPattern string `json:"suggestedPersistPattern"`
}

type askQuestionInteraction struct {
	Questions []question `json:"questions"`
}

type question struct {
	Question      string           `json:"question"`
	Options       []questionOption `json:"options"`
	IsMultiSelect bool             `json:"isMultiSelect"`
	ID            string           `json:"id,omitempty"`
	Header        string           `json:"header,omitempty"`
}

type questionOption struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func isOpenStepStatus(status string) bool {
	switch status {
	case contracts.AntigravityStepStatusGenerating, contracts.AntigravityStepStatusQueued, contracts.AntigravityStepStatusPending,
		contracts.AntigravityStepStatusRunning, contracts.AntigravityStepStatusWaiting:
		return true
	default:
		return false
	}
}

func decodeStep(raw json.RawMessage) (antigravityStep, error) {
	var step antigravityStep
	if err := json.Unmarshal(raw, &step); err != nil {
		return step, err
	}
	if step.Type == "" || step.Status == "" {
		return step, fmt.Errorf("step has no type or status")
	}
	return step, nil
}

func interactionKind(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return "", fmt.Errorf("waiting step has no requested interaction")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	var kinds []string
	for key := range fields {
		kinds = append(kinds, key)
	}
	if len(kinds) != 1 {
		return "", fmt.Errorf("requested interaction holds %d kinds", len(kinds))
	}
	switch kinds[0] {
	case "permission", "askQuestion":
		return kinds[0], nil
	default:
		return kinds[0], fmt.Errorf("unknown Antigravity interaction %q", kinds[0])
	}
}

func stepDeclined(step antigravityStep) bool {
	err := step.Error
	text := strings.Join([]string{err.UserErrorMessage, err.ModelErrorMessage, err.ShortError, err.FullError, err.Details}, "\n")
	return strings.Contains(text, "user denied permission")
}
