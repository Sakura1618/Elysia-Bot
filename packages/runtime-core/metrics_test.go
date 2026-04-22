package runtimecore

import (
	"reflect"
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
		"bot_platform_event_throughput_total",
		"bot_platform_handler_latency_ms",
		"bot_platform_job_dispatch_ready_total",
		"bot_platform_job_recoveries_total",
		"bot_platform_job_status_total",
		"bot_platform_plugin_errors_total",
		"bot_platform_queue_lag",
		"bot_platform_schedule_due_ready_total",
		"bot_platform_schedule_recoveries_total",
		"bot_platform_subprocess_failures_total",
	}
	sort.Strings(wantFamilies)
	if !reflect.DeepEqual(gotFamilies, wantFamilies) {
		t.Fatalf("expected baseline metric families %v, got %v", wantFamilies, gotFamilies)
	}

	for _, expected := range []string{
		"bot_platform_event_throughput_total 1",
		"bot_platform_handler_latency_ms{plugin_id=\"plugin-echo\"} 12",
		"bot_platform_plugin_errors_total{plugin_id=\"plugin-echo\"} 1",
		"bot_platform_subprocess_failures_total{plugin_id=\"plugin-echo\",failure_stage=\"dispatch\",failure_reason=\"response_timeout\"} 1",
		"bot_platform_queue_lag 3",
		"bot_platform_job_status_total{status=\"retrying\"} 2",
		"bot_platform_job_recoveries_total 1",
		"bot_platform_schedule_recoveries_total 1",
		"bot_platform_job_dispatch_ready_total 1",
		"bot_platform_schedule_due_ready_total 4",
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
	metrics.RecordEventThroughput()
	metrics.RecordHandlerLatency("plugin-echo", 12*time.Millisecond)
	metrics.RecordPluginError("plugin-echo")
	metrics.RecordSubprocessFailure("plugin-echo", "dispatch", "response_timeout")
	metrics.SetQueueLag(3)
	metrics.SetJobStatusCount(JobStatusRetrying, 2)
	metrics.IncrementJobRecoveries()
	metrics.IncrementScheduleRecoveries()
	metrics.SetJobDispatchReadyCount(1)
	metrics.SetScheduleDueReadyCount(4)
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
