package orchestration

// FindRootJobs returns jobs with no dependencies.
func FindRootJobs(plan *Plan) []*Job {
	var roots []*Job
	for _, job := range plan.Jobs {
		if len(job.Dependencies) == 0 {
			roots = append(roots, job)
		}
	}
	return roots
}

// FindAllDependents returns ALL jobs that depend on the given job (not filtered).
func FindAllDependents(job *Job, plan *Plan) []*Job {
	var dependents []*Job
	for _, candidate := range plan.Jobs {
		for _, dep := range candidate.Dependencies {
			if dep != nil && dep.ID == job.ID {
				dependents = append(dependents, candidate)
				break
			}
		}
	}
	return dependents
}
