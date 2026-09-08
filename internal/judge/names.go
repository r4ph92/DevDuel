package judge

import (
	"errors"
	"fmt"
	"regexp"
)

// namePrefix marks everything the judge creates, so an interrupted run can be
// swept up by label or by name.
const namePrefix = "devduel-"

// jobIDPattern is what a job id may contain. It has to survive being part of
// a docker object name, and it ends up in logs and error messages.
var jobIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// names is everything one job creates.
type names struct {
	network string
	runner  string
	tester  string
	labels  map[string]string
}

func namesFor(jobID string) names {
	base := namePrefix + jobID

	return names{
		network: base,
		runner:  base + "-runner",
		tester:  base + "-tester",
		labels:  map[string]string{"devduel.job": jobID},
	}
}

func (j Job) validate() error {
	switch {
	case j.ID == "":
		return errors.New("job id is required")
	case !jobIDPattern.MatchString(j.ID):
		return fmt.Errorf("job id %q must be lowercase letters, digits and single hyphens", j.ID)
	case j.Spec == nil:
		return errors.New("job has no challenge spec")
	case j.Workspace == "":
		return errors.New("job has no workspace directory")
	}
	return nil
}
