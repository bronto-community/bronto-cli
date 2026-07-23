// Package cichecks backs the ruleset<->workflow tripwire: it verifies that
// every status check the branch-protection ruleset marks REQUIRED is
// actually produced by a job in the CI workflow. This is the failure that
// wedged development after the #63 CI-hardening work — the ruleset required
// a check named "generate-clean" while ci.yml had renamed that job to
// "repo-gates", so every PR waited forever on a context that would never
// report (GitHub treats a missing required context as perpetually pending).
package cichecks

import (
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// WorkflowJobs returns the set of job ids defined in a GitHub Actions
// workflow file.
func WorkflowJobs(workflowYAML []byte) (map[string]bool, error) {
	var wf struct {
		Jobs map[string]yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflowYAML, &wf); err != nil {
		return nil, err
	}
	jobs := make(map[string]bool, len(wf.Jobs))
	for id := range wf.Jobs {
		jobs[id] = true
	}
	return jobs, nil
}

// BaseJob strips a matrix suffix from a status-check context to recover the
// job id backing it: "test (ubuntu-latest)" -> "test". A matrix job's
// default check name is "<job> (<matrix values>)", so the substring before
// " (" is the job id; contexts without a matrix suffix are returned as-is.
func BaseJob(context string) string {
	if i := strings.Index(context, " ("); i >= 0 {
		return context[:i]
	}
	return context
}

// UnbackedChecks returns the required checks whose backing job is absent
// from jobs. An empty result means every required check maps to a real CI
// job. (It intentionally checks the backing job, not the exact matrix
// expansion: a renamed or deleted job — the wedge that actually happened —
// is fully caught, without brittle matrix-name reconstruction.)
func UnbackedChecks(required []string, jobs map[string]bool) []string {
	var bad []string
	for _, c := range required {
		if !jobs[BaseJob(c)] {
			bad = append(bad, c)
		}
	}
	return bad
}
