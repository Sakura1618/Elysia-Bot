package pluginsdk

type RolloutPhase string

const (
	RolloutPhaseStable    RolloutPhase = "stable"
	RolloutPhaseCandidate RolloutPhase = "candidate"
	RolloutPhaseCanary    RolloutPhase = "canary"
	RolloutPhaseRollback  RolloutPhase = "rollback"
)

type RolloutStatus string

const (
	RolloutStatusPrepared   RolloutStatus = "prepared"
	RolloutStatusRejected   RolloutStatus = "rejected"
	RolloutStatusCanarying  RolloutStatus = "canarying"
	RolloutStatusActivated  RolloutStatus = "activated"
	RolloutStatusRolledBack RolloutStatus = "rolled_back"
)

type RolloutRecord struct {
	PluginID         string        `json:"pluginId"`
	StableVersion    string        `json:"stableVersion,omitempty"`
	ActiveVersion    string        `json:"activeVersion,omitempty"`
	CurrentVersion   string        `json:"currentVersion"`
	CandidateVersion string        `json:"candidateVersion"`
	Phase            RolloutPhase  `json:"phase,omitempty"`
	Status           RolloutStatus `json:"status"`
	Reason           string        `json:"reason,omitempty"`
}
