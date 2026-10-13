package gateway

const maxUpstreamRetries = 3

// PlaygroundRetryEvent is private to /api/playground. Compatible public APIs
// never receive this event or replay an already delivered response fragment.
const PlaygroundRetryEvent = "tokenswoop.retry"

// Authentication/mapping failures may use another channel but cannot be fixed
// by resending to the same channel. Caller errors and client disconnects stop.
func retryUpstream(r Result) (retry, reuseChannel bool) {
	switch r.Code {
	case "upstream_authentication_failed", "upstream_model_or_endpoint_missing":
		return true, false
	case "upstream_outcome_unknown", "upstream_timeout", "upstream_rate_limited",
		"upstream_service_error", "invalid_upstream_response", "invalid_stream_content_type",
		"incomplete_stream", "stream_idle_timeout", "upstream_canceled",
		"usage_missing", "usage_unconfirmed", "tool_usage_missing":
		return true, true
	default:
		return false, false
	}
}
