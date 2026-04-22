package runtimecore

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMetricsRegistryLocksActive2BaselineFamilies(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsRegistry()
	recordActive2BaselineMetrics(metrics)

	output := metrics.RenderPrometheus()
	gotFamilies := collectPrometheusFamilies(output)
	wantFamilies := []string{
		"bot_platform_job_count",
		"bot_platform_job_dispatch_ready_count",
		"bot_platform_job_queue_active_count",
		"bot_platform_job_recovery_total",
		"bot_platform_runtime_dispatch_last_duration_ms",
		"bot_platform_runtime_dispatch_total",
		"bot_platform_schedule_due_ready_count",
		"bot_platform_schedule_recovery_total",
		"bot_platform_subprocess_dispatch_last_duration_ms",
		"bot_platform_subprocess_dispatch_total",
		"bot_platform_subprocess_failure_total",
		"bot_platform_workflow_instance_count",
		"bot_platform_workflow_transition_total",
	}
	for _, family := range wantFamilies {
		if !slicesContains(gotFamilies, family) {
			t.Fatalf("expected baseline metric family %q, got %v", family, gotFamilies)
		}
	}

	for _, expected := range []string{
		"bot_platform_runtime_dispatch_total{plugin_id=\"plugin-echo\",operation=\"event\",outcome=\"success\"} 1",
		"bot_platform_runtime_dispatch_last_duration_ms{plugin_id=\"plugin-echo\",operation=\"event\"} 12",
		"bot_platform_subprocess_dispatch_total{plugin_id=\"plugin-echo\",operation=\"event\",outcome=\"error\"} 1",
		"bot_platform_subprocess_dispatch_last_duration_ms{plugin_id=\"plugin-echo\",operation=\"event\"} 12",
		"bot_platform_subprocess_failure_total{plugin_id=\"plugin-echo\",operation=\"event\",failure_stage=\"dispatch\",failure_reason=\"response_timeout\"} 1",
		"bot_platform_job_queue_active_count 3",
		"bot_platform_job_count{status=\"retrying\"} 2",
		"bot_platform_job_recovery_total{outcome=\"recovered_running\"} 1",
		"bot_platform_schedule_recovery_total{outcome=\"recomputed_due_at\"} 1",
		"bot_platform_job_dispatch_ready_count 1",
		"bot_platform_schedule_due_ready_count 4",
		"bot_platform_workflow_instance_count{status=\"waiting_event\"} 1",
		"bot_platform_workflow_transition_total{plugin_id=\"plugin-echo\",outcome=\"started\"} 1",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected metrics output to contain %q, got %s", expected, output)
		}
	}
}

func TestMetricsRegistryForbidsHighCardinalityContextLabels(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsRegistry()
	recordActive2BaselineMetrics(metrics)

	output := metrics.RenderPrometheus()
	for _, forbidden := range []string{"trace_id", "event_id", "run_id", "correlation_id"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("expected prometheus output to omit forbidden high-cardinality label %q, got %s", forbidden, output)
		}
	}
}

func recordActive2BaselineMetrics(metrics *MetricsRegistry) {
	metrics.RecordRuntimeDispatch("plugin-echo", "event", "success", 12*time.Millisecond)
	metrics.RecordSubprocessDispatch("plugin-echo", "event", "error", 12*time.Millisecond)
	metrics.RecordSubprocessFailure("plugin-echo", "event", "dispatch", "response_timeout")
	metrics.SetJobQueueActiveCount(3)
	metrics.SetJobCount(JobStatusRetrying, 2)
	metrics.IncrementJobRecovery("recovered_running")
	metrics.IncrementScheduleRecovery("recomputed_due_at")
	metrics.SetJobDispatchReadyCount(1)
	metrics.SetScheduleDueReadyCount(4)
	metrics.SetWorkflowInstanceCount(WorkflowRuntimeStatusWaitingEvent, 1)
	metrics.RecordWorkflowTransition("plugin-echo", "started")
}

func collectPrometheusFamilies(output string) []string {
	families := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		family := line
		if index := strings.IndexAny(family, "{ "); index >= 0 {
			family = family[:index]
		}
		families[family] = struct{}{}
	}

	result := make([]string, 0, len(families))
	for family := range families {
		result = append(result, family)
	}
	sort.Strings(result)
	return result
}

func slicesContains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
