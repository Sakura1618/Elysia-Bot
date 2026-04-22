package runtimecore

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type MetricsRegistry struct {
	mu                 sync.RWMutex
	eventThroughput    int
	handlerLatencyMs   map[string]float64
	pluginErrors       map[string]int
	subprocessFailures map[string]int
	queueLag           int
	jobStatusCounts    map[JobStatus]int
	jobRecoveries      int
	scheduleRecoveries int
	jobDispatchReady   int
	scheduleDueReady   int
}

func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		handlerLatencyMs:   make(map[string]float64),
		pluginErrors:       make(map[string]int),
		subprocessFailures: make(map[string]int),
		jobStatusCounts:    make(map[JobStatus]int),
	}
}

func (m *MetricsRegistry) RecordEventThroughput() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventThroughput++
}

func (m *MetricsRegistry) RecordHandlerLatency(pluginID string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlerLatencyMs[pluginID] = float64(duration.Milliseconds())
}

func (m *MetricsRegistry) RecordPluginError(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pluginErrors[pluginID]++
}

func (m *MetricsRegistry) RecordSubprocessFailure(pluginID string, stage string, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s|%s|%s", pluginID, stage, reason)
	m.subprocessFailures[key]++
}

func (m *MetricsRegistry) SetQueueLag(lag int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queueLag = lag
}

func (m *MetricsRegistry) SetJobStatusCount(status JobStatus, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobStatusCounts[status] = count
}

func (m *MetricsRegistry) IncrementJobRecoveries() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobRecoveries++
}

func (m *MetricsRegistry) IncrementScheduleRecoveries() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleRecoveries++
}

func (m *MetricsRegistry) SetJobDispatchReadyCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobDispatchReady = count
}

func (m *MetricsRegistry) SetScheduleDueReadyCount(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleDueReady = count
}

func (m *MetricsRegistry) RenderPrometheus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lines := []string{
		fmt.Sprintf("bot_platform_event_throughput_total %d", m.eventThroughput),
		fmt.Sprintf("bot_platform_queue_lag %d", m.queueLag),
		fmt.Sprintf("bot_platform_job_recoveries_total %d", m.jobRecoveries),
		fmt.Sprintf("bot_platform_schedule_recoveries_total %d", m.scheduleRecoveries),
		fmt.Sprintf("bot_platform_job_dispatch_ready_total %d", m.jobDispatchReady),
		fmt.Sprintf("bot_platform_schedule_due_ready_total %d", m.scheduleDueReady),
	}

	pluginIDs := sortedKeys(m.handlerLatencyMs)
	for _, pluginID := range pluginIDs {
		lines = append(lines, fmt.Sprintf("bot_platform_handler_latency_ms{plugin_id=%q} %.0f", pluginID, m.handlerLatencyMs[pluginID]))
	}

	for _, pluginID := range sortedStringKeys(m.pluginErrors) {
		lines = append(lines, fmt.Sprintf("bot_platform_plugin_errors_total{plugin_id=%q} %d", pluginID, m.pluginErrors[pluginID]))
	}

	for _, key := range sortedStringKeys(m.subprocessFailures) {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		lines = append(lines, fmt.Sprintf("bot_platform_subprocess_failures_total{plugin_id=%q,failure_stage=%q,failure_reason=%q} %d", parts[0], parts[1], parts[2], m.subprocessFailures[key]))
	}

	statuses := make([]string, 0, len(m.jobStatusCounts))
	for status := range m.jobStatusCounts {
		statuses = append(statuses, string(status))
	}
	sort.Strings(statuses)
	for _, status := range statuses {
		lines = append(lines, fmt.Sprintf("bot_platform_job_status_total{status=%q} %d", status, m.jobStatusCounts[JobStatus(status)]))
	}

	return strings.Join(lines, "\n") + "\n"
}

func sortedKeys(items map[string]float64) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(items map[string]int) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
