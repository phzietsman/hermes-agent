package anthropic

// Pattern tables for the error classifier, copied from agent/error_classifier.py. All patterns are
// lowercase substrings matched against the lowercased error text.

var (
	freeTierRefusalPatterns = []string{"model_not_supported_on_free_tier", "not available on the free tier"}

	billingPatterns = append([]string{
		"insufficient credits", "insufficient_quota", "insufficient balance", "credit balance",
		"credits exhausted", "credits have been exhausted", "requires available credits",
		"account balance is too low", "no usable credits", "top up your credits", "payment required",
		"billing hard limit", "exceeded your current quota", "account is deactivated", "plan does not include",
		"out of extra usage", "out of funds", "run out of funds", "balance_depleted",
		"budget limit exceeded",
	}, append(append([]string{}, freeTierRefusalPatterns...), "hard billing limit")...)

	// unverifiedBillingPatterns are not proof of exhaustion: Anthropic returns the same "out of extra
	// usage" body for a content-filter rejection. The verdict stays billing but is marked unverified so
	// surfaces hedge.
	unverifiedBillingPatterns = []string{"out of extra usage"}

	billingErrorCodes = setOf(
		"insufficient_quota", "billing_not_active", "payment_required", "insufficient_credits",
		"no_usable_credits", "balance_depleted", "model_not_supported_on_free_tier",
		"member_spend_cap_exceeded", "terminal_quota_exhausted", "personal-team-blocked:spending-limit",
		"credit_balance_exhausted", "organization_spend_limit_exceeded",
		"organization_usage_limit_exceeded", "project_spend_limit_exceeded",
		"insufficient_credits_for_paid_model",
	)

	rateLimitPatterns = []string{
		"rate limit", "rate_limit", "too many requests", "throttled", "requests per minute",
		"tokens per minute", "requests per day", "try again in", "please retry after",
		"resource exhausted", "resource_exhausted", "resource-exhausted", "resourceexhausted",
		"rate increased too quickly", "throttlingexception", "too many concurrent requests",
		"servicequotaexceededexception", "throttling",
	}

	// overloadedPatterns: server busy, credential fine. Back off on the same key, never rotate.
	overloadedPatterns = []string{
		"overloaded", "temporarily overloaded", "service is temporarily overloaded",
		"service may be temporarily overloaded", "server is overloaded", "server overloaded",
		"server overload", "server_overload",
		"service overloaded", "service is overloaded", "upstream overloaded", "currently overloaded",
		"upstream model provider is temporarily unavailable. please try again in a moment.",
		"at capacity", "over capacity",
	}

	usageLimitPatterns         = []string{"usage limit", "quota", "limit exceeded", "key limit exceeded"}
	usageLimitTransientSignals = []string{
		"try again", "retry", "resets at", "reset in", "resets in", "reset after", "available in",
		"wait", "requests remaining", "periodic", "window", "per minute", "per second",
	}

	payloadTooLargePatterns = []string{
		"request entity too large", "payload too large", "error code: 413", "request_too_large",
		"request exceeds the maximum size",
	}

	imageTooLargePatterns = []string{
		"image exceeds", "image too large", "image_too_large", "image size exceeds", "image dimensions exceed",
		"dimensions exceed max allowed size", "max allowed size: 8000", "media exceeds", "media too large",
		"patches after processing", "make sure your payload is below", "streamreadconstraints.getmaxstringlength",
	}

	imageCorruptPatterns = []string{
		"invalid png image", "invalid jpeg image", "base64 string of provided image cannot be decoded",
		"downloaded response does not contain a valid jpg, png, webp, or ico image",
	}

	multimodalToolContentPatterns = []string{
		"text is not set", "tool message content must be a string", "tool content must be a string",
		"tool message must be a string", "expected string, got list", "expected string, got array",
		"tool_call.content must be string", "tool.content.str", "input should be a valid string",
		"chatcompletionrequesttoolmessagecontent",
	}

	memoryCeilingPatterns = []string{
		"memory guard", "memory limit exceeded", "memory_guard_tier", "dynamic ceiling",
		"memory ceiling", "available memory", "out of memory", "insufficient memory",
		"prefill would require", "predicted peak would", "prefill safety cap", "metal_cap",
	}
	memoryCeilingErrorCodes = setOf("prefill_memory_exceeded", "prefill_memory_aborted", "omlx_prefill_memory_exceeded")

	// contextOverflowPatterns: the bare "max_tokens" is load-bearing, the output-cap retry path keys off it.
	contextOverflowPatterns = []string{
		"context length", "context size", "maximum context", "token limit", "too many tokens",
		"reduce the length", "exceeds the limit", "context window", "prompt is too long",
		"prompt exceeds max length", "max_tokens", "maximum number of tokens",
		"exceeds the max_model_len", "max_model_len", "prompt length", "input is too long", "maximum model length",
		"context length exceeded", "truncating input",
		"slot context", "n_ctx_slot",
		"超过最大长度", "上下文长度",
		"tokens in request more than max tokens allowed",
		"input is too long", "max input token", "input token", "exceeds the maximum number of input tokens",
		"maximum allowed input length",
	}

	modelNotFoundPatterns = []string{
		"is not a valid model", "invalid model", "model not found", "model_not_found", "does not exist",
		"no such model", "unknown model", "unsupported model", "no endpoints found that support tool use",
		"is no longer free",
	}

	// invalidMessageBodyPatterns are deterministic rejections of the transcript (a content-less assistant
	// stub after a dead stream). Not overflow: the input may be tiny and compression cannot invent a turn.
	invalidMessageBodyPatterns = []string{
		"must have non-empty content", "messages must have non-empty", "invalid_request_body",
		"text content blocks must be non-empty", "content field is required",
		"messages: at least one message is required", "no user query found",
	}

	roleAlternationPatterns = []string{
		"roles must alternate", "role must alternate", "must alternate between",
		"consecutive user messages", "consecutive messages with the same role",
		"consecutive messages of the same role", "same role in a row", "multiple user messages in a row",
		"adjacent messages with the same role",
	}

	malformedToolArgsPatterns = []string{
		"invalid tool call arguments", "invalid tool_call arguments", "invalid tool_calls arguments",
		"invalid function call arguments", "invalid function_call arguments",
		"tool call arguments are invalid", "tool_call arguments are invalid",
		"function call arguments are invalid", "function_call arguments are invalid",
	}

	requestValidationPatterns = []string{
		"unknown parameter", "unsupported parameter", "unrecognized request argument",
		"invalid_request_error", "unknown_parameter", "unsupported_parameter",
	}

	paramRejectionWords = []string{"not supported", "unsupported", "unknown", "unrecognized"}

	// thinkingMutationWords are Anthropic's thinking-block 400 wordings.
	thinkingMutationWords = []string{"signature", "cannot be modified", "must remain as they were"}

	providerPolicyBlockedPatterns = []string{
		"no endpoints available matching your guardrail", "no endpoints available matching your data policy",
		"no endpoints found matching your data policy",
	}

	accountPolicyBlockPatterns = []string{"blocked for a previous policy violation"}

	// contentPolicyBlockedPatterns are per-prompt safety-filter blocks: deterministic for the unchanged
	// request, so fall back immediately. Never a generic word like "policy", which collides with billing/auth.
	contentPolicyBlockedPatterns = []string{
		"flagged for possible cybersecurity risk", "trusted access for cyber",
		"violates our usage policies", "violates openai's usage policies", "your request was flagged by",
		"prompt was flagged by our safety", "responses cannot be generated due to safety",
		"content_filter", "responsibleaipolicyviolation", "new_sensitive",
		"content exists risk",
	}

	authPatterns = []string{
		"invalid api key", "invalid_api_key", "gateway_auth_failed", "authentication", "unauthorized",
		"forbidden", "invalid token", "token expired", "token revoked", "access denied",
		"failed to extract accountid from token",
	}

	emptyProviderResponsePatterns = []string{
		"returned an empty response", "empty response despite retries", "provider returned an empty response",
		"model returning empty responses", "empty response stream",
	}

	timeoutMessagePatterns = []string{
		"timed out", "turn timed out", "request timed out", "deadline exceeded", "operation timed out",
		"upstream timed out",
		"i/o timeout", // Go's net error wording
	}

	connectionMessagePatterns = []string{
		"connection refused", "econnrefused", "no route to host", "network is unreachable", "network unreachable",
		"name or service not known", "temporary failure in name resolution", "nodename nor servname provided",
		"getaddrinfo failed", "getaddrinfo enotfound", "eai_again",
		"fetch failed", "failed to fetch",
		"upstream connect error",
		"no such host", // Go's resolver wording
	}

	serverDisconnectPatterns = []string{
		"server disconnected", "peer closed connection", "connection reset by peer", "connection was closed",
		"network connection lost", "unexpected eof", "incomplete chunked read",
	}

	sslCertVerifyPatterns = []string{
		"certificate verify failed", "certificate_verify_failed", "unable to get local issuer certificate",
		"self-signed certificate", "self signed certificate", "certificate has expired",
		"hostname mismatch, certificate is not valid", "unable to verify the first certificate",
		"certificate signed by unknown authority", "x509:", // Go's crypto/x509 wording
	}

	sslTransientPatterns = []string{
		"bad record mac", "ssl alert", "tls alert", "ssl handshake failure", "tlsv1 alert", "sslv3 alert",
		"bad_record_mac", "ssl_alert", "tls_alert", "tls_alert_internal_error", "[ssl:",
		"remote error: tls", // Go's crypto/tls wording
	}

	// upstreamBlockedPatterns: a 403 body written by a WAF/CDN/proxy rather than the provider's API.
	upstreamBlockedPatterns = []string{
		"your request was blocked", "request blocked", "sorry, you have been blocked",
		"enable javascript and cookies to continue", "cdn-cgi/challenge-platform", "cf-browser-verification",
		"challenge-error-text", "__cf_chl", "cf-error-details", "attention required! | cloudflare",
	}

	streamRenderErrorPatterns = []string{"error rendering", "rendering prompt", "jinja template", "jinja render"}

	reasoningRequiredMarkers = []string{
		"mandatory", "cannot be disabled", "can't be disabled", "must be enabled", "is required",
		"always enabled", "cannot be turned off",
	}

	unsupportedParamMarkers = []string{
		"unsupported parameter", "unsupported_parameter", "not supported", "does not support",
		"doesn't support", "is deprecated for this model",
		"unknown parameter", "unrecognized request argument", "unrecognized parameter",
		"invalid parameter", "extra inputs are not permitted",
		"invalid option: expected one of",
	}

	resetFields  = []string{"resets_in_seconds", "resets_at", "reset_at", "retry_after"}
	resetHeaders = []string{"retry-after", "x-ratelimit-reset"}
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}
