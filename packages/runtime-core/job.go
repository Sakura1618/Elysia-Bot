package runtimecore

import (
	"time"

	pluginsdk "github.com/ohmyopencode/bot-platform/packages/plugin-sdk"
)

type JobStatus = pluginsdk.JobStatus
type JobReasonCode = pluginsdk.JobReasonCode

const (
	JobStatusPending   = pluginsdk.JobStatusPending
	JobStatusRunning   = pluginsdk.JobStatusRunning
	JobStatusRetrying  = pluginsdk.JobStatusRetrying
	JobStatusPaused    = pluginsdk.JobStatusPaused
	JobStatusCancelled = pluginsdk.JobStatusCancelled
	JobStatusFailed    = pluginsdk.JobStatusFailed
	JobStatusDead      = pluginsdk.JobStatusDead
	JobStatusDone      = pluginsdk.JobStatusDone

	JobReasonCodeRuntimeRestart  = pluginsdk.JobReasonCodeRuntimeRestart
	JobReasonCodeWorkerAbandoned = pluginsdk.JobReasonCodeWorkerAbandoned
	JobReasonCodeTimeout         = pluginsdk.JobReasonCodeTimeout
	JobReasonCodeDispatchRetry   = pluginsdk.JobReasonCodeDispatchRetry
	JobReasonCodeDispatchDead    = pluginsdk.JobReasonCodeDispatchDead
	JobReasonCodeExecutionRetry  = pluginsdk.JobReasonCodeExecutionRetry
	JobReasonCodeExecutionDead   = pluginsdk.JobReasonCodeExecutionDead
)

type Job = pluginsdk.Job

func NewJob(id, jobType string, maxRetries int, timeout time.Duration) Job {
	return pluginsdk.NewJob(id, jobType, maxRetries, timeout)
}
