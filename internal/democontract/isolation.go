package democontract

import (
	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
)

const IsolationSuite = "todo-isolated/v1"

// IsolationRun contains checks from disposable local fixtures, never live traffic.
type IsolationRun struct {
	Release      ecg.Release            `json:"release"`
	Suite        string                 `json:"suite"`
	Observations []evidence.Observation `json:"observations"`
}

func IsolationRequirements(release ecg.Release) []evidence.Requirement {
	var out []evidence.Requirement
	for _, scenario := range []string{"add", "add-invalid", "list", "complete", "delete", "list~slow"} {
		if release.Service == "store" && scenario == "add-invalid" {
			continue
		} // validation stops before the store
		for _, flag := range []string{"off", "on"} {
			out = append(out, evidence.Requirement{Series: evidence.Series{
				Path: []ecg.Release{release}, Scenario: evidence.Definition{Name: scenario, Version: "1"},
				Checks: []evidence.Definition{{Name: IsolationSuite, Version: "1"}},
				Flags:  map[string]string{"express": flag}, Source: "isolated", Environment: "disposable-fixture", Schema: "todo/v1",
			}})
		}
	}
	return out
}
