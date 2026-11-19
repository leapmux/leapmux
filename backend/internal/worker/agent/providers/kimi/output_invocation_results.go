package kimi

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type kimiAgentResultEvidence struct {
	childID, taskID string
	actualProfile   string
	status          bgtask.Status
}

// parseKimiAgentResult reads only fixed builtin headers. Report bodies supply no identity.
func parseKimiAgentResult(output string, completion agent.MessageCompletion) (kimiAgentResultEvidence, bool) {
	lines := strings.SplitN(output, "\n", 7)
	field := func(index int, prefix string) string {
		if index >= len(lines) || !strings.HasPrefix(lines[index], prefix) {
			return ""
		}
		return strings.TrimPrefix(lines[index], prefix)
	}
	if completion == agent.MessageCompletionComplete && len(lines) == 7 && strings.HasPrefix(lines[0], "task_id: ") {
		taskID, childID, profile := field(0, "task_id: "), field(2, "agent_id: "), field(3, "actual_subagent_type: ")
		if kimiCheckID("task", taskID) == nil && kimiCheckID("child agent", childID) == nil && childID != kimiMainAgentID &&
			lines[1] == "status: running" && profile != "" && !strings.ContainsAny(profile, "\r\n") && lines[4] == "automatic_notification: true" && lines[5] == "" {
			return kimiAgentResultEvidence{taskID: taskID, childID: childID, actualProfile: profile}, true
		}
		return kimiAgentResultEvidence{}, false
	}
	if len(lines) < 6 {
		return kimiAgentResultEvidence{}, false
	}
	childID, profile, reason := field(0, "agent_id: "), field(1, "actual_subagent_type: "), field(3, "stop_reason: ")
	if kimiCheckID("child agent", childID) != nil || childID == kimiMainAgentID || profile == "" || strings.ContainsAny(profile, "\r\n") {
		return kimiAgentResultEvidence{}, false
	}
	status := bgtask.StatusUnspecified
	if completion == agent.MessageCompletionComplete && lines[2] == "status: completed" {
		switch reason {
		case "completed":
			if lines[4] != "" || lines[5] != "[summary]" {
				return kimiAgentResultEvidence{}, false
			}
		case "repeat_breaker":
			if len(lines) != 7 || lines[4] != "notice: The subagent was stopped by the repeat breaker after issuing the same tool call repeatedly. The summary below is its handoff, not a finished result." || lines[5] != "" || !strings.HasPrefix(lines[6], "[summary]\n") {
				return kimiAgentResultEvidence{}, false
			}
		default:
			return kimiAgentResultEvidence{}, false
		}
		status = bgtask.StatusSucceeded
	} else if (completion == agent.MessageCompletionError || completion == agent.MessageCompletionInterrupted) && lines[2] == "status: failed" && lines[4] == "" && strings.HasPrefix(lines[5], "subagent error: ") {
		switch reason {
		case "cancelled", "stopped":
			status = bgtask.StatusStopped
		case "timed_out", "filtered", "max_tokens", "max_steps", "provider_error", "no_final_message", "error":
			status = bgtask.StatusFailed
		default:
			return kimiAgentResultEvidence{}, false
		}
	}
	return kimiAgentResultEvidence{childID: childID, status: status, actualProfile: profile}, status != bgtask.StatusUnspecified
}

type kimiSwarmResultEvidence struct {
	// finished survives ambiguous unescaped member bodies; outcomes then remain unknown.
	finished bool
	outcomes map[string]bgtask.Status
}

var kimiSwarmAttribute = regexp.MustCompile(`^ (mode|agent_id|item|state|outcome|stop_reason)="((?:[^"<>&]|&amp;|&quot;|&lt;|&gt;)*)"`)

const kimiSwarmResumeHint = "<resume_hint>Call AgentSwarm with resume_agent_ids using the agent_id values in this result to continue unfinished work.</resume_hint>\n"

func parseKimiSwarmResult(input json.RawMessage, output string, refs map[string]struct{}) kimiSwarmResultEvidence {
	var args struct {
		Items  []json.RawMessage `json:"items"`
		Resume map[string]string `json:"resume_agent_ids"`
	}
	if json.Unmarshal(input, &args) != nil || len(args.Items)+len(args.Resume) == 0 || len(refs) == 0 {
		return kimiSwarmResultEvidence{}
	}
	expected := len(args.Items) + len(args.Resume)
	output = strings.TrimSuffix(output, "\n")
	if !strings.HasPrefix(output, "<agent_swarm_result>\n<summary>") || !strings.HasSuffix(output, "\n</agent_swarm_result>") {
		return kimiSwarmResultEvidence{}
	}
	content := strings.TrimSuffix(strings.TrimPrefix(output, "<agent_swarm_result>\n<summary>"), "\n</agent_swarm_result>")
	summary, members, found := strings.Cut(content, "</summary>\n")
	if !found {
		return kimiSwarmResultEvidence{}
	}
	counts := make(map[string]int)
	total, previous := 0, -1
	for _, part := range strings.Split(summary, ", ") {
		label, raw, found := strings.Cut(part, ": ")
		order := map[string]int{"completed": 0, "failed": 1, "aborted": 2}
		index, known := order[label]
		value, err := strconv.Atoi(raw)
		if !found || !known || index <= previous || err != nil || value <= 0 || value > expected-total || strconv.Itoa(value) != raw {
			return kimiSwarmResultEvidence{}
		}
		counts[label], total, previous = value, total+value, index
	}
	if total != expected {
		return kimiSwarmResultEvidence{}
	}
	members = strings.TrimPrefix(members, kimiSwarmResumeHint)
	evidence := kimiSwarmResultEvidence{finished: true}
	outcomes, seenCounts := make(map[string]bgtask.Status), make(map[string]int)
	records := 0
	anonymous := 0
	// An extra closing tag can move a later apparent header into an opaque body.
	// The first header remains unambiguous and must always validate.
	extraClosings := strings.Count(members, "</subagent>") > expected
	refuseMember := func() kimiSwarmResultEvidence {
		if records > 0 && extraClosings {
			return evidence
		}
		return kimiSwarmResultEvidence{}
	}
	decode := strings.NewReplacer("&quot;", "\"", "&lt;", "<", "&gt;", ">", "&amp;", "&")
	for members != "" {
		if !strings.HasPrefix(members, "<subagent ") {
			return refuseMember()
		}
		attributes, rest, found := strings.Cut(strings.TrimPrefix(members, "<subagent"), ">")
		if !found {
			return refuseMember()
		}
		fields := make(map[string]string)
		for attributes != "" {
			match := kimiSwarmAttribute.FindStringSubmatch(attributes)
			if match == nil {
				return refuseMember()
			}
			if _, duplicate := fields[match[1]]; duplicate {
				return refuseMember()
			}
			fields[match[1]] = decode.Replace(match[2])
			attributes = strings.TrimPrefix(attributes, match[0])
		}
		if mode, present := fields["mode"]; present && mode != "resume" {
			return refuseMember()
		}
		if state, present := fields["state"]; present && state != "started" && state != "not_started" {
			return refuseMember()
		}
		status := bgtask.StatusUnspecified
		switch fields["outcome"] {
		case "completed":
			status = bgtask.StatusSucceeded
		case "failed":
			status = bgtask.StatusFailed
		case "aborted":
			status = bgtask.StatusStopped
		default:
			return refuseMember()
		}
		if childID := fields["agent_id"]; childID != "" {
			if _, valid := refs[childID]; !valid || kimiCheckID("child agent", childID) != nil {
				return refuseMember()
			}
			if _, duplicate := outcomes[childID]; duplicate {
				return refuseMember()
			}
			outcomes[childID] = status
		} else if fields["state"] != "not_started" {
			return refuseMember()
		} else if status == bgtask.StatusSucceeded {
			return refuseMember()
		} else {
			anonymous++
		}
		body, remainder, found := strings.Cut(rest, "</subagent>")
		if !found {
			return refuseMember()
		}
		for _, reserved := range []string{"<subagent", "</subagent", "<agent_swarm_result", "</agent_swarm_result", "<summary", "</summary", "<resume_hint", "</resume_hint"} {
			if strings.Contains(body, reserved) {
				return evidence
			}
		}
		if remainder != "" && !strings.HasPrefix(remainder, "\n") {
			return evidence
		}
		members = strings.TrimPrefix(remainder, "\n")
		seenCounts[fields["outcome"]]++
		records++
	}
	if records != expected || len(counts) != len(seenCounts) {
		return refuseMember()
	}
	for key, count := range counts {
		if seenCounts[key] != count {
			return kimiSwarmResultEvidence{}
		}
	}
	if missing := len(refs) - len(outcomes); missing != 0 {
		if missing > 0 && missing <= anonymous {
			return evidence
		}
		return kimiSwarmResultEvidence{}
	}
	evidence.outcomes = outcomes
	return evidence
}
