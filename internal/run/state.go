package run

// State is the authoritative lifecycle of a run. A conversation is not a run.
type State string

const (
	Created          State = "CREATED"
	Contextualising  State = "CONTEXTUALISING"
	Planning         State = "PLANNING"
	Executing        State = "EXECUTING"
	Observing        State = "OBSERVING"
	Verifying        State = "VERIFYING"
	AwaitingApproval State = "AWAITING_APPROVAL"
	// Committing closes the sandbox and records success. It does not create a Git commit.
	Committing      State = "COMMITTING"
	Succeeded       State = "SUCCEEDED"
	Paused          State = "PAUSED"
	PausedBudget    State = "PAUSED_BUDGET"
	Cancelled       State = "CANCELLED"
	FailedRetryable State = "FAILED_RETRYABLE"
	FailedTerminal  State = "FAILED_TERMINAL"
)

// IsTerminal reports states that no worker should leave.
func IsTerminal(s State) bool {
	switch s {
	case Succeeded, Cancelled, FailedTerminal:
		return true
	default:
		return false
	}
}

// escapes are legal from every non-terminal state.
var escapes = []State{Paused, PausedBudget, Cancelled, FailedRetryable, FailedTerminal}

var forward = map[State][]State{
	Created:          {Contextualising},
	Contextualising:  {Planning},
	Planning:         {Executing, Observing, Verifying, AwaitingApproval},
	Executing:        {Observing, AwaitingApproval, Verifying},
	Observing:        {Planning, Executing, Verifying, AwaitingApproval},
	Verifying:        {AwaitingApproval, Committing, Succeeded},
	AwaitingApproval: {Executing, Committing, Observing},
	Committing:       {Succeeded},
	Paused:           {Planning},
	PausedBudget:     {Planning},
	FailedRetryable:  {Planning},
}

// Can reports whether a single compare-and-swap transition is legal.
func Can(from, to State) bool {
	if from == to || from == "" || to == "" {
		return false
	}
	if !IsTerminal(from) {
		for _, escape := range escapes {
			if to == escape {
				return true
			}
		}
	}
	for _, next := range forward[from] {
		if next == to {
			return true
		}
	}
	return false
}
