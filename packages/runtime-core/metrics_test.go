package runtimecore

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsRegistryRendersPrometheusOutput(t *testing.T) {
	t.Parallel()

	metrics := NewMetricsRegistry()
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

	output := metrics.RenderPrometheus()
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
