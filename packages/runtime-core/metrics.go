package runtimecore

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type dispatchMetricKey struct {
	PluginID  string
	Operation string
	Outcome   string
}

type dispatchDurationKey struct {
	PluginID  string
	Operation string
}

type subprocessFailureMetricKey struct {
	PluginID      string
	Operation     string
	FailureStage  string
	FailureReason string
}

type workflowTransitionMetricKey struct {
	PluginID string
	Outcome  string
}

type MetricsRegistry struct {
	mu                       sync.RWMutex
	eventThroughputTotal     int
	handlerLatencyMs         map[string]float64
	pluginErrors             map[string]int
	queueLag                 int
	runtimeDispatchTotals    map[dispatchMetricKey]int
	runtimeDispatchLastMs    map[dispatchDurationKey]float64
	subprocessDispatchTotals map[dispatchMetricKey]int
	subprocessDispatchLastMs map[dispatchDurationKey]float64
	subprocessFailures       map[subprocessFailureMetricKey]int
	jobQueueActiveCount      int
	jobCounts                map[JobStatus]int
	jobRecoveries            map[string]int
	scheduleRecoveries       map[string]int
	jobDispatchReadyCount    int
	scheduleDueReadyCount    int
	workflowInstanceCounts   map[WorkflowRuntimeStatus]int
	workflowTransitionTotals map[workflowTransitionMetricKey]int
}

func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		handlerLatencyMs:         make(map[string]float64),
		pluginErrors:             make(map[string]int),
		runtimeDispatchTotals:    make(map[dispatchMetricKey]int),
		runtimeDispatchLastMs:    make(map[dispatchDurationKey]float64),
		subprocessDispatchTotals: make(map[dispatchMetricKey]int),
		subprocessDispatchLastMs: make(map[dispatchDurationKey]float64),
		subprocessFailures:       make(map[subprocessFailureMetricKey]int),
		jobCounts:                make(map[JobStatus]int),
		jobRecoveries:            make(map[string]int),
		scheduleRecoveries:       make(map[string]int),
		workflowInstanceCounts:   make(map[WorkflowRuntimeStatus]int),
		workflowTransitionTotals: make(map[workflowTransitionMetricKey]int),
	}
}

func (m *MetricsRegistry) RecordEventThroughput() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventThroughputTotal++
}

func (m *MetricsRegistry) RecordHandlerLatency(pluginID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlerLatencyMs[normalizeMetricLabel(pluginID, "unknown_plugin")] = float64(duration.Milliseconds())
}

func (m *MetricsRegistry) RecordPluginError(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pluginErrors[normalizeMetricLabel(pluginID, "unknown_plugin")]++
}

func (m *MetricsRegistry) SetQueueLag(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queueLag = count
	m.jobQueueActiveCount = count
}

func (m *MetricsRegistry) SetJobStatusCount(status JobStatus, count int) {
	m.SetJobCount(status, count)
}

func (m *MetricsRegistry) IncrementJobRecoveries() {
	m.IncrementJobRecovery("recovered")
}

func (m *MetricsRegistry) IncrementScheduleRecoveries() {
	m.IncrementScheduleRecovery("recovered")
}

func (m *MetricsRegistry) RecordRuntimeDispatch(pluginID string, operation string, outcome string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pluginID = normalizeMetricLabel(pluginID, "unknown_plugin")
	operation = normalizeMetricLabel(operation, "unknown_operation")
	outcome = normalizeMetricLabel(outcome, "unknown_outcome")
	key := dispatchMetricKey{
		PluginID:  pluginID,
		Operation: operation,
		Outcome:   outcome,
	}
	m.runtimeDispatchTotals[key]++
	m.handlerLatencyMs[pluginID] = float64(duration.Milliseconds())
	if outcome == "error" {
		m.pluginErrors[pluginID]++
	}
	m.runtimeDispatchLastMs[dispatchDurationKey{PluginID: key.PluginID, Operation: key.Operation}] = float64(duration.Milliseconds())
}

func (m *MetricsRegistry) RecordSubprocessDispatch(pluginID string, operation string, outcome string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pluginID = normalizeMetricLabel(pluginID, "unknown_plugin")
	operation = normalizeMetricLabel(operation, "unknown_operation")
	outcome = normalizeMetricLabel(outcome, "unknown_outcome")
	key := dispatchMetricKey{
		PluginID:  pluginID,
		Operation: operation,
		Outcome:   outcome,
	}
	m.subprocessDispatchTotals[key]++
	m.handlerLatencyMs[pluginID] = float64(duration.Milliseconds())
	if outcome == "error" {
		m.pluginErrors[pluginID]++
	}
	m.subprocessDispatchLastMs[dispatchDurationKey{PluginID: key.PluginID, Operation: key.Operation}] = float64(duration.Milliseconds())
}

func (m *MetricsRegistry) RecordSubprocessFailure(pluginID string, operation string, stage string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := subprocessFailureMetricKey{
		PluginID:      normalizeMetricLabel(pluginID, "unknown_plugin"),
		Operation:     normalizeMetricLabel(operation, "unknown_operation"),
		FailureStage:  normalizeMetricLabel(stage, "unknown_stage"),
		FailureReason: normalizeMetricLabel(reason, "unknown_reason"),
	}
	m.subprocessFailures[key]++
}

func (m *MetricsRegistry) SetJobQueueActiveCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobQueueActiveCount = count
	m.queueLag = count
}

func (m *MetricsRegistry) SetJobCount(status JobStatus, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobCounts[status] = count
}

func (m *MetricsRegistry) IncrementJobRecovery(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobRecoveries[normalizeMetricLabel(outcome, "unknown_outcome")]++
}

func (m *MetricsRegistry) IncrementScheduleRecovery(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleRecoveries[normalizeMetricLabel(outcome, "unknown_outcome")]++
}

func (m *MetricsRegistry) SetJobDispatchReadyCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobDispatchReadyCount = count
}

func (m *MetricsRegistry) SetScheduleDueReadyCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleDueReadyCount = count
}

func (m *MetricsRegistry) SetWorkflowInstanceCount(status WorkflowRuntimeStatus, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workflowInstanceCounts[status] = count
}

func (m *MetricsRegistry) RecordWorkflowTransition(pluginID string, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := workflowTransitionMetricKey{
		PluginID: normalizeMetricLabel(pluginID, "unknown_plugin"),
		Outcome:  normalizeMetricLabel(outcome, "unknown_outcome"),
	}
	m.workflowTransitionTotals[key]++
}

func (m *MetricsRegistry) RenderPrometheus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lines := []string{
		fmt.Sprintf("bot_platform_event_throughput_total %d", m.eventThroughputTotal),
		fmt.Sprintf("bot_platform_job_queue_active_count %d", m.jobQueueActiveCount),
		fmt.Sprintf("bot_platform_job_dispatch_ready_count %d", m.jobDispatchReadyCount),
		fmt.Sprintf("bot_platform_schedule_due_ready_count %d", m.scheduleDueReadyCount),
		fmt.Sprintf("bot_platform_queue_lag %d", m.queueLag),
	}

	for _, pluginID := range sortedStringKeys(m.pluginErrors) {
		lines = append(lines, fmt.Sprintf("bot_platform_plugin_errors_total{plugin_id=%q} %d", pluginID, m.pluginErrors[pluginID]))
	}

	for _, pluginID := range sortedStringFloatKeys(m.handlerLatencyMs) {
		lines = append(lines, fmt.Sprintf("bot_platform_handler_latency_ms{plugin_id=%q} %.0f", pluginID, m.handlerLatencyMs[pluginID]))
	}

	for _, key := range sortedDispatchMetricKeys(m.runtimeDispatchTotals) {
		lines = append(lines, fmt.Sprintf("bot_platform_runtime_dispatch_total{plugin_id=%q,operation=%q,outcome=%q} %d", key.PluginID, key.Operation, key.Outcome, m.runtimeDispatchTotals[key]))
	}

	for _, key := range sortedDispatchDurationKeys(m.runtimeDispatchLastMs) {
		lines = append(lines, fmt.Sprintf("bot_platform_runtime_dispatch_last_duration_ms{plugin_id=%q,operation=%q} %.0f", key.PluginID, key.Operation, m.runtimeDispatchLastMs[key]))
	}

	for _, key := range sortedDispatchMetricKeys(m.subprocessDispatchTotals) {
		lines = append(lines, fmt.Sprintf("bot_platform_subprocess_dispatch_total{plugin_id=%q,operation=%q,outcome=%q} %d", key.PluginID, key.Operation, key.Outcome, m.subprocessDispatchTotals[key]))
	}

	for _, key := range sortedDispatchDurationKeys(m.subprocessDispatchLastMs) {
		lines = append(lines, fmt.Sprintf("bot_platform_subprocess_dispatch_last_duration_ms{plugin_id=%q,operation=%q} %.0f", key.PluginID, key.Operation, m.subprocessDispatchLastMs[key]))
	}

	for _, key := range sortedSubprocessFailureMetricKeys(m.subprocessFailures) {
		lines = append(lines, fmt.Sprintf("bot_platform_subprocess_failure_total{plugin_id=%q,operation=%q,failure_stage=%q,failure_reason=%q} %d", key.PluginID, key.Operation, key.FailureStage, key.FailureReason, m.subprocessFailures[key]))
		lines = append(lines, fmt.Sprintf("bot_platform_subprocess_failures_total{plugin_id=%q,failure_stage=%q,failure_reason=%q} %d", key.PluginID, key.FailureStage, key.FailureReason, m.subprocessFailures[key]))
	}

	for _, status := range sortedJobStatuses(m.jobCounts) {
		lines = append(lines, fmt.Sprintf("bot_platform_job_count{status=%q} %d", status, m.jobCounts[JobStatus(status)]))
		lines = append(lines, fmt.Sprintf("bot_platform_job_status_total{status=%q} %d", status, m.jobCounts[JobStatus(status)]))
	}

	for _, outcome := range sortedStringKeys(m.jobRecoveries) {
		lines = append(lines, fmt.Sprintf("bot_platform_job_recovery_total{outcome=%q} %d", outcome, m.jobRecoveries[outcome]))
	}

	for _, outcome := range sortedStringKeys(m.scheduleRecoveries) {
		lines = append(lines, fmt.Sprintf("bot_platform_schedule_recovery_total{outcome=%q} %d", outcome, m.scheduleRecoveries[outcome]))
	}

	lines = append(lines, fmt.Sprintf("bot_platform_job_recoveries_total %d", sumMetricCounts(m.jobRecoveries)))
	lines = append(lines, fmt.Sprintf("bot_platform_schedule_recoveries_total %d", sumMetricCounts(m.scheduleRecoveries)))

	lines = append(lines, fmt.Sprintf("bot_platform_job_dispatch_ready_total %d", m.jobDispatchReadyCount))
	lines = append(lines, fmt.Sprintf("bot_platform_schedule_due_ready_total %d", m.scheduleDueReadyCount))

	for _, status := range sortedWorkflowStatuses(m.workflowInstanceCounts) {
		lines = append(lines, fmt.Sprintf("bot_platform_workflow_instance_count{status=%q} %d", status, m.workflowInstanceCounts[WorkflowRuntimeStatus(status)]))
	}

	for _, key := range sortedWorkflowTransitionMetricKeys(m.workflowTransitionTotals) {
		lines = append(lines, fmt.Sprintf("bot_platform_workflow_transition_total{plugin_id=%q,outcome=%q} %d", key.PluginID, key.Outcome, m.workflowTransitionTotals[key]))
	}

	return strings.Join(lines, "\n") + "\n"
}

func normalizeMetricLabel(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func sortedStringKeys(items map[string]int) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringFloatKeys(items map[string]float64) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sumMetricCounts(items map[string]int) int {
	total := 0
	for _, count := range items {
		total += count
	}
	return total
}

func sortedDispatchMetricKeys(items map[dispatchMetricKey]int) []dispatchMetricKey {
	keys := make([]dispatchMetricKey, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i int, j int) bool {
		if keys[i].PluginID != keys[j].PluginID {
			return keys[i].PluginID < keys[j].PluginID
		}
		if keys[i].Operation != keys[j].Operation {
			return keys[i].Operation < keys[j].Operation
		}
		return keys[i].Outcome < keys[j].Outcome
	})
	return keys
}

func sortedDispatchDurationKeys(items map[dispatchDurationKey]float64) []dispatchDurationKey {
	keys := make([]dispatchDurationKey, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i int, j int) bool {
		if keys[i].PluginID != keys[j].PluginID {
			return keys[i].PluginID < keys[j].PluginID
		}
		return keys[i].Operation < keys[j].Operation
	})
	return keys
}

func sortedSubprocessFailureMetricKeys(items map[subprocessFailureMetricKey]int) []subprocessFailureMetricKey {
	keys := make([]subprocessFailureMetricKey, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i int, j int) bool {
		if keys[i].PluginID != keys[j].PluginID {
			return keys[i].PluginID < keys[j].PluginID
		}
		if keys[i].Operation != keys[j].Operation {
			return keys[i].Operation < keys[j].Operation
		}
		if keys[i].FailureStage != keys[j].FailureStage {
			return keys[i].FailureStage < keys[j].FailureStage
		}
		return keys[i].FailureReason < keys[j].FailureReason
	})
	return keys
}

func sortedJobStatuses(items map[JobStatus]int) []string {
	statuses := make([]string, 0, len(items))
	for status := range items {
		statuses = append(statuses, string(status))
	}
	sort.Strings(statuses)
	return statuses
}

func sortedWorkflowStatuses(items map[WorkflowRuntimeStatus]int) []string {
	statuses := make([]string, 0, len(items))
	for status := range items {
		statuses = append(statuses, string(status))
	}
	sort.Strings(statuses)
	return statuses
}

func sortedWorkflowTransitionMetricKeys(items map[workflowTransitionMetricKey]int) []workflowTransitionMetricKey {
	keys := make([]workflowTransitionMetricKey, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i int, j int) bool {
		if keys[i].PluginID != keys[j].PluginID {
			return keys[i].PluginID < keys[j].PluginID
		}
		return keys[i].Outcome < keys[j].Outcome
	})
	return keys
}
