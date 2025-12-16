package cmd

import "github.com/mattsolo1/grove-flow/pkg/orchestration"

// ExportedFindRootJobs exports findRootJobs for use by the status_tui package
func ExportedFindRootJobs(plan *orchestration.Plan) []*orchestration.Job {
	return findRootJobs(plan)
}

// ExportedFindAllDependents exports findAllDependents for use by the status_tui package
func ExportedFindAllDependents(job *orchestration.Job, plan *orchestration.Plan) []*orchestration.Job {
	return findAllDependents(job, plan)
}

// ExportedVerifyRunningJobStatus exports VerifyRunningJobStatus for use by the status_tui package
func ExportedVerifyRunningJobStatus(plan *orchestration.Plan) {
	VerifyRunningJobStatus(plan)
}

// ExportedCompleteJob exports completeJob for use by the status_tui package
func ExportedCompleteJob(job *orchestration.Job, plan *orchestration.Plan, silent bool) error {
	return completeJob(job, plan, silent)
}
