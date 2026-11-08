"""Generate golden fixtures for the Go error classifier by running the Python classifier.

usage (from the repo root, with the test environment's interpreter):
    python scripts/port/gen_anthropic_errors_golden.py internal/provider/anthropic/testdata

Each case is a genuine `anthropic` SDK exception (a status error built from a real httpx response, or an
error raised while the SDK reads an SSE stream with an error event) classified by
`agent.error_classifier.classify_api_error` with provider="anthropic". The Go classifier must reach the same
verdict. Corpus text avoids the wordings of providers the Go port drops (Codex, xAI, Nous, OpenRouter).
"""
import json
import pathlib
import sys
import time

import anthropic
import httpx

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[2]))

from agent.error_classifier import classify_api_error  # noqa: E402
from agent.retry_utils import parse_retry_after_seconds, reset_delay_from_message  # noqa: E402

CLIENT = anthropic.Anthropic(api_key="x", base_url="http://t", max_retries=0)
OPUS = "claude-opus-4-5"
HAIKU = "claude-haiku-4-5"
SONNET = "claude-sonnet-4-6"


def err(msg, typ="invalid_request_error", **extra):
    return {"type": "error", "error": {"type": typ, "message": msg, **extra}}


CASES = []


def add(name, status=None, body=None, text=None, headers=None, model=OPUS, tokens=0, ctx=200000, msgs=0, stream=None):
    CASES.append(dict(name=name, status=status, body=body, text=text, headers=headers or {}, model=model,
                      tokens=tokens, ctx=ctx, msgs=msgs, stream=stream))


# ---- 400 ----
for name, msg in {
    "prompt_too_long": "prompt is too long: 250000 tokens > 200000 maximum",
    "max_tokens_too_big": "max_tokens: 100000 > 64000, which is the maximum allowed number of output tokens for claude-opus-4-5",
    "context_window": "input length and `max_tokens` exceed context limit: 190000 + 20000 > 200000",
    "tool_result_missing": "messages.3: `tool_use` ids were found without `tool_result` blocks immediately after: toolu_01",
    "blank_text": "messages.2: text content blocks must be non-empty",
    "blank_text_ws": "messages: text content blocks must contain non-whitespace text",
    "image_big": "image exceeds 5 MB maximum: 7340032 bytes > 5242880 bytes",
    "image_dims": "image dimensions exceed max allowed size: 8000 pixels",
    "image_invalid": "Could not process image",
    "invalid_png": "invalid png image data",
    "credit_low": "Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.",
    "extra_usage": "You're out of extra usage. Add more at claude.ai/settings/usage and keep going.",
    "long_ctx_beta": "The long context beta is not yet available for this subscription.",
    "role_alt": "messages: roles must alternate between \"user\" and \"assistant\", but found multiple \"user\" roles in a row",
    "bad_param": "temperature: Unsupported parameter for this model",
    "unknown_field": "messages.0.content.0: Unknown parameter: 'foo'",
    "extra_inputs": "tools.0.input_schema: extra inputs are not permitted",
    "model_bad": "model: claude-opus-9 is not a valid model",
    "model_dne": "The model `claude-x` does not exist",
    "rate_in_400": "This request would exceed your organization's rate limit of 30,000 input tokens per minute",
    "overloaded_in_400": "The service is temporarily overloaded",
    "thinking_sig": "messages.1.content.0: Invalid `signature` in `thinking` block",
    "thinking_modified": "messages.1.content.0: `thinking` blocks in the latest assistant message cannot be modified. These blocks must remain as they were in the original response.",
    "thinking_enabled_budget": "thinking.enabled.budget_tokens: Input should be greater than or equal to 1024",
    "reasoning_mandatory": "Reasoning is mandatory for this endpoint and cannot be disabled",
    "thinking_unsupported": "thinking is not supported for this model",
    "reasoning_effort_unsupported": "Unrecognized request argument supplied: reasoning_effort",
    "safety_block": "Your request was flagged by our safety systems",
    "cyber": "This request was flagged for possible cybersecurity risk",
    "content_filter": "content_filter triggered",
    "memory": "out of memory while allocating prefill",
    "invalid_tool_args": "invalid tool call arguments",
    "bare_error": "Error",
    "bare_short": "Bad request",
    "prompt_cache_retention": "prompt_cache_retention is not supported",
    "descriptive_long": "messages.5.content: this is a long and descriptive rejection message that is not generic",
    "empty": "",
}.items():
    add("400_" + name, 400, err(msg))
    add("400_" + name + "_large", 400, err(msg), tokens=150000, ctx=200000, msgs=120)
add("400_no_type_body", 400, {"message": "flat message prompt is too long"})
add("400_detail_string", 400, {"detail": "request validation failed: invalid"})
add("400_html", 400, text="<html><body>Bad Request</body></html>")
add("400_empty_body", 400, text="")
add("400_bare_json_generic_large", 400, {"error": {"type": "invalid_request_error", "message": "Error"}}, tokens=170000, msgs=10)
add("400_bare_small_ctx", 400, {"error": {"type": "invalid_request_error", "message": "x"}}, tokens=70000, ctx=128000, msgs=100)
add("400_bare_small_ctx_tokens", 400, {"error": {"type": "invalid_request_error", "message": "x"}}, tokens=90000, ctx=128000, msgs=3)
add("400_code_param", 400, {"error": {"type": "invalid_request_error", "message": "bad", "code": "unsupported_parameter"}})
add("400_code_unknown_param", 400, {"error": {"code": "unknown_parameter", "message": "bad thing"}})
add("400_param_reasoning", 400, {"error": {"type": "invalid_request_error", "message": "", "param": "reasoning.effort"}})
add("400_invalid_request_body_code", 400, {"error": {"code": "invalid_request_body", "message": "boom"}})
add("400_memory_code", 400, {"error": {"code": "prefill_memory_exceeded", "message": "no"}})
add("400_nested_code", 400, {"error": {"message": json.dumps({"code": "insufficient_quota"}), "type": "400"}})

# ---- 401 / 402 / 403 / 404 / 408 / 413 / 422 ----
add("401", 401, err("invalid x-api-key", "authentication_error"))
add("401_oauth_revoked", 401, err("OAuth token has been revoked.", "authentication_error"))
add("401_no_body", 401, text="")
add("402", 402, err("Payment required", "billing_error"))
add("402_usage_limit_transient", 402, err("Usage limit reached. Try again in 5 minutes."))
add("402_quota_plain", 402, err("Quota exceeded for this key"))
add("403", 403, err("Request not allowed", "permission_error"))
add("403_cf", 403, text="<html>Attention Required! | Cloudflare</html>")
add("403_blocked", 403, text="Sorry, you have been blocked")
add("403_billing", 403, err("Your credit balance is too low", "permission_error"))
add("403_key_limit", 403, err("key limit exceeded (monthly)", "permission_error"))
add("403_upstream_unavailable", 403, {"error": {"code": "upstream_unavailable", "message": "later"}})
add("403_forbidden_word", 403, err("forbidden by policy", "permission_error"))
add("404", 404, err("model: claude-opus-9", "not_found_error"))
add("404_does_not_exist", 404, err("The model claude-x does not exist", "not_found_error"))
add("404_billing_code", 404, {"error": {"code": "insufficient_credits_for_paid_model", "message": "gone"}})
add("404_plain", 404, text="Not Found")
add("404_credit", 404, err("credit balance is too low", "not_found_error"))
add("408", 408, err("Request timeout"))
add("413", 413, err("Request exceeds the maximum allowed number of bytes.", "request_too_large"))
add("413_text", 413, text="Request Entity Too Large")
add("422_image", 422, {"detail": [{"type": "string_type", "loc": ["body", "messages", 1, "content", "str"], "msg": "x", "input": "y"}]})
add("422_plain", 422, err("unprocessable"))
add("422_image_corrupt", 422, err("invalid jpeg image"))

# ---- 429 ----
add("429", 429, err("This request would exceed your organization's rate limit", "rate_limit_error"))
add("429_retry_after_hdr", 429, err("Rate limit hit", "rate_limit_error"), headers={"retry-after": "17"})
add("429_retry_after_big", 429, err("Rate limit hit", "rate_limit_error"), headers={"retry-after": "100000"})
add("429_retry_after_zero", 429, err("Rate limit hit", "rate_limit_error"), headers={"retry-after": "0"})
add("429_msg_reset", 429, err("Rate limit exceeded, retry after 12 seconds", "rate_limit_error"))
add("429_msg_resets_in", 429, err("Weekly limit reached. Resets in 4hr 5min", "rate_limit_error"))
add("429_resets_in_field", 429, {"error": {"type": "rate_limit_error", "message": "slow", "resets_in_seconds": 90}})
add("429_body_retry_after", 429, {"error": {"type": "rate_limit_error", "message": "slow", "retry_after": 33}})
add("429_quota_wall", 429, err("You have exceeded your usage limit", "rate_limit_error"))
add("429_quota_wall_billing", 429, err("insufficient_quota", "rate_limit_error"))
add("429_quota_wall_with_signal", 429, err("Usage limit reached, try again later", "rate_limit_error"))
add("429_quota_wall_hdr", 429, err("Usage limit reached", "rate_limit_error"), headers={"retry-after": "60"})
add("429_usage_limit_reached_code", 429, {"error": {"type": "rate_limit_error", "code": "usage_limit_reached", "message": "no more"}})
add("429_overloaded_words", 429, err("Service is temporarily overloaded", "rate_limit_error"))
add("429_long_context", 429, err("Long context requests require extra usage, which is not enabled", "rate_limit_error"))
add("429_billing_code", 429, {"error": {"code": "terminal_quota_exhausted", "message": "hard cap"}})
add("429_extra_usage_only", 429, err("You are out of extra usage", "rate_limit_error"))
add("429_explicit_and_quota", 429, err("Rate limit exceeded: limit exceeded for the quota", "rate_limit_error"))
add("429_x_ratelimit_reset_hdr", 429, err("Usage limit", "rate_limit_error"), headers={"x-ratelimit-reset": "30"})
add("429_quota_reset_delay", 429, err("slow down quotaResetDelay: 1500ms", "rate_limit_error"))

# ---- 5xx ----
add("500", 500, err("Internal server error", "api_error"))
add("500_overflow", 500, err("context window exceeded"))
add("500_empty_resp", 500, err("returned an empty response"))
add("500_validation", 500, err("unknown parameter foo"))
add("500_invalid_request", 500, err("bad", "invalid_request_error"))
add("500_memory", 500, err("memory guard tripped"))
add("502", 502, text="<html>502 Bad Gateway</html>")
add("503", 503, err("Service unavailable", "api_error"))
add("503_overflow", 503, err("prompt is too long"))
add("504", 504, text="Gateway Timeout")
add("520_cf", 520, text="error code: 520")
add("529", 529, err("Overloaded", "overloaded_error"))
add("529_retry_after", 529, err("Overloaded", "overloaded_error"), headers={"retry-after": "7"})
add("529_overflow", 529, err("max_tokens exceeds"))
add("599", 599, text="weird")
add("418", 418, text="teapot")
add("451", 451, err("unavailable for legal reasons"))
add("500_body_only_retry_after", 500, {"error": {"type": "api_error", "message": "x", "retry_after": 12}})

# ---- stream error events (status-less in the loop's eyes) ----
for name, event in {
    "overloaded": {"type": "overloaded_error", "message": "Overloaded"},
    "api_error": {"type": "api_error", "message": "Internal server error"},
    "rate_limit": {"type": "rate_limit_error", "message": "Number of request tokens has exceeded your per-minute rate limit"},
    "invalid_request": {"type": "invalid_request_error", "message": "prompt is too long: 250000 tokens > 200000 maximum"},
    "authentication": {"type": "authentication_error", "message": "invalid x-api-key"},
    "permission": {"type": "permission_error", "message": "forbidden"},
    "not_found": {"type": "not_found_error", "message": "model: claude-x"},
    "unknown_type": {"type": "weird_error", "message": "something odd"},
    "no_type": {"message": "disconnected"},
    "billing_msg": {"type": "invalid_request_error", "message": "Your credit balance is too low"},
    "content": {"type": "invalid_request_error", "message": "content_filter blocked it"},
}.items():
    add("stream_error_" + name, stream=event)
    add("stream_error_" + name + "_large", stream=event, tokens=150000, msgs=250)

# ---- models: reasoning floors apply to status-less disconnect messages only (SDK transport errors) ----
for m in (OPUS, HAIKU, SONNET, "claude-fable-5-1", "claude-opus-5-5", "anthropic/claude-sonnet-5-5", "claude-3-5-sonnet"):
    add("400_model_" + m.replace("/", "_"), 400, err("Error"), model=m, tokens=170000)
    add("429_model_" + m.replace("/", "_"), 429, err("rate limit"), model=m)


def build_error(c):
    if c["stream"] is not None:
        sse = "event: error\ndata: " + json.dumps({"type": "error", "error": c["stream"]}) + "\n\n"
        sse = ("event: message_start\ndata: " + json.dumps({"type": "message_start", "message": {
            "id": "m", "type": "message", "role": "assistant", "model": "x", "content": [], "stop_reason": None,
            "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}}) + "\n\n") + sse
        transport = httpx.MockTransport(lambda r: httpx.Response(200, headers={"content-type": "text/event-stream"}, content=sse.encode()))
        client = anthropic.Anthropic(api_key="x", base_url="http://t", max_retries=0, http_client=httpx.Client(transport=transport))
        try:
            with client.messages.stream(model="claude-opus-4-5", max_tokens=10, messages=[{"role": "user", "content": "x"}]) as s:
                for _ in s:
                    pass
        except Exception as exc:  # the SDK's own error object
            return exc
        raise AssertionError("stream did not fail: " + c["name"])
    headers = dict(c["headers"])
    if c["text"] is not None:
        resp = httpx.Response(c["status"], content=c["text"].encode(), headers=headers, request=httpx.Request("POST", "http://t/v1/messages"))
    else:
        headers.setdefault("content-type", "application/json")
        resp = httpx.Response(c["status"], content=json.dumps(c["body"]).encode(), headers=headers, request=httpx.Request("POST", "http://t/v1/messages"))
    return CLIENT._make_status_error_from_response(resp)


def run(c):
    error = build_error(c)
    before = time.time()
    ce = classify_api_error(error, provider="anthropic", model=c["model"], approx_tokens=c["tokens"],
                            context_length=c["ctx"], num_messages=c["msgs"])
    reset_at = ce.error_context.get("reset_at")
    return {
        "reason": ce.reason.value, "status_code": ce.status_code, "message": ce.message, "retryable": ce.retryable,
        "should_compress": ce.should_compress, "should_rotate_credential": ce.should_rotate_credential,
        "should_fallback": ce.should_fallback, "billing_unverified": ce.billing_unverified,
        "reset_after": None if reset_at is None else round(reset_at - before, 2),
    }


# Retry helpers: numeric forms only (HTTP-date depends on the clock and is unit-tested in Go).
RETRY_AFTER_VALUES = ["0", "5", "17.5", " 30 ", "-4", "abc", "", "1e2", "100000"]
MESSAGES = [
    "retry after 12 seconds", "Retry 5s", "retry after 2.5 sec", "resets in 4hr 5min", "resets in 2 hours 5 minutes",
    "reset in 30s", "resets in 90 seconds", "quotaResetDelay: 1500ms", 'quotaResetDelay":"3s"', "resets_in_seconds: 30995",
    "'resets_in_seconds': 12", "retry after 10 seconds, resets in 5hr", "no hint here", "resets in soon", "RETRY AFTER 8 SECS",
    "resets in 1 hour", "resets in 7m", "resets in 45", "please retry 3s later",
]


def main(outdir: str) -> None:
    out = pathlib.Path(outdir)
    out.mkdir(parents=True, exist_ok=True)
    cases = []
    for c in CASES:
        expected = run(c)
        cases.append({k: v for k, v in c.items() if v is not None or k in ("status",)} | {"expected": expected})
    retry = {
        "retry_after": [{"in": v, "want": parse_retry_after_seconds(v)} for v in RETRY_AFTER_VALUES],
        "messages": [{"in": m, "want": reset_delay_from_message(m)} for m in MESSAGES],
    }
    (out / "errors.json").write_text(json.dumps({"cases": cases, "retry": retry}, indent=1, sort_keys=True) + "\n")
    print(f"wrote {len(cases)} classifier cases to {out}")


if __name__ == "__main__":
    main(sys.argv[1])
