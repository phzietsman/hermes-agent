# PORT_TESTS.md — Python test ledger and coverage floors for the Go port

Rules: see `PORT.md` (Testing buckets). Before porting a module, resolve every row of its group below to one bucket:
`behaviour` (port to Go, table-driven), `compiler` (delete, one-line reason), `dropped` (feature not in Stage 1).
Put the Go counterpart (package and test name) in the last column for `behaviour` rows.

## Method (reproducible)

- Test env: `python -m pm.build_env --source . --out <dir> --group dev --group test --all-extras` (extras are needed: without them `aiohttp` and others fail at collection).
- `coverage==7.10.7` installed outside that env and put on `PYTHONPATH` (the env itself is not mutated). `COVERAGE_CORE=sysmon`, `--branch`, Python 3.14.7.
- Each owned test file runs in its own `pytest` subprocess (timeout 180s), as `scripts/run_tests.sh` does; data is combined per group.
- Scripts: `scripts/port/{groups,map_tests,ledger,run_all}.py`. `groups.py` lists each group's source globs. `ledger.py` assigns every test file to the single group whose modules it imports most (`hermes_constants` and `agent.secret_scope` are ignored for ownership because most of the suite imports them).
- Floor = the combined line+branch percent below, measured over the group's source files with only that group's owned tests. A Go package for the group must meet or beat it, measured with `go test -coverprofile` (`-covermode=atomic`). Go statements the compiler makes unreachable or unnecessary do not need tests, so this is a floor on behaviour coverage, not a line-for-line target.
- Not run through `scripts/run_tests.sh` because it cannot be wrapped with coverage. Each file is still isolated, so the leakage guarantees are the same.

## Baseline (Python, 2026-11-08, commit `f9f92f1bd5`)

| Group | Tests (files) | Statements | Branches | Line % | Line+branch % (floor) | Failed files | Timeouts |
|---|---:|---:|---:|---:|---:|---:|---:|
| config | 618 | 6059 | 2296 | 86.1 | **84.6** | 8 | 3 |
| session | 375 | 4590 | 1612 | 89.4 | **87.3** | 0 | 0 |
| skills | 57 | 1901 | 688 | 84.7 | **83.1** | 0 | 0 |
| anthropic | 108 | 2270 | 826 | 86.3 | **83.8** | 2 | 0 |
| prompt | 39 | 1334 | 416 | 83.4 | **81.8** | 1 | 0 |
| loop | 116 | 7415 | 2586 | 77.8 | **74.5** | 0 | 0 |
| compaction | 105 | 5470 | 2084 | 85.2 | **82.2** | 1 | 0 |
| approval | 60 | 1477 | 634 | 91.1 | **90.2** | 1 | 0 |
| tools-core | 233 | 4464 | 1632 | 78.8 | **76.6** | 5 | 0 |
| memory | 22 | 1409 | 332 | 89.4 | **87.4** | 0 | 0 |
| learning | 3 | 361 | 62 | 86.4 | **82.3** | 0 | 0 |
| cron | 186 | 8723 | 2810 | 88.8 | **87.2** | 0 | 0 |
| gateway-core | 556 | 8142 | 2656 | 85.7 | **83.8** | 0 | 0 |
| telegram | 54 | 5343 | 1918 | 63.7 | **60.3** | 1 | 0 |
| discord | 36 | 5433 | 1898 | 57.9 | **54.2** | 0 | 0 |

Total owned test files: 2568. Failed or timed-out files still contribute the coverage they reached before failing; `config` has 8 failures and 3 timeouts, `tools-core` 5, `anthropic` 2, the rest at most 1 (several are already on the known-failing list, DECISIONS D23/D26). The ledger step for each module must note which of its files fail on Python and why.

Test files that import none of the Stage 1 modules (other than `hermes_constants`) are not listed: they test dropped features, TypeScript-facing code or tooling, and are deleted with Python at cutover.

## Ledger

Columns: file | bucket (`behaviour` / `compiler` / `dropped`, empty = unresolved) | Go counterpart or reason.
Extra status `later`: the file was assigned to this group by import counting but belongs to a module that is ported by a later task; that task re-buckets it. Behaviour rows that cover only part of a Python file say `subset`.

### config (618 files, floor 84.6%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/acp_adapter/test_acp_commands.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/acp_adapter/test_acp_mcp_discovery.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/acp_adapter/test_named_provider_catalogs.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/acp_adapter/test_session.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/agent/lsp/test_broken_set.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/lsp/test_install_and_lint_fixes.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/lsp/test_root_policy.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_actual_auxiliary_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_anthropic_adapter_extra_headers.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_anthropic_prompt_cache_policy.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_api_max_retries_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_aux_progress_streaming.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_auxiliary_client_azure_foundry.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_auxiliary_client_ssl_verify.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_cost_controls.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_input_budget.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_tool_call_guard.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_toolset_restriction.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_usage.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_bitwarden_secrets.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_cache_disabled_on_stubs.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_codex_app_server_integration.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_codex_gpt55_autoraise_notice.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_command_secret_source.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_command_token_source.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_config_defaults.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_max_attempts_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_context_compressor.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_credential_pool.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_credential_pool_profile_oauth_fork.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_credential_pool_seed_existing_env_sources.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_credential_rotation_route_settings.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_credits_notices_toggle.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_curator.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_custom_provider_ca_probes.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_custom_provider_extra_headers_client.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_deadline.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_empty_response_guard.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_env_loader_applied_homes.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_env_loader_reload_restore.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_env_loader_secret_sources.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_fast_mode_auto.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_file_mutation_verifier.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_gemini_native_reservation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_glm_context_window_resolution.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_image_generate_parallel.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_invalid_context_length_warning.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_iron_proxy.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_malformed_tool_arguments.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_memory_provider_init.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_moa_cold_start_cache.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_moa_reasoning_effort.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_moa_trace_streamed_capture.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_model_streaming_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_models_dev.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_nous_welcome_client_contract.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_ollama_num_ctx.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_openrouter_response_cache.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_openrouter_silent_default_purge.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_outbound_webhooks.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_per_model_provider_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_per_model_threshold_init_ordering.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_plugin_context_engine_init.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_plugin_llm.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_plugin_llm_task_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_plugin_stream_hooks.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_preflight_compression_cap_e2e.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_proactive_prune_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_prompt_builder.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_prompt_cache_ttl_propagation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_provider_attribution_headers.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_provider_reset_cooldown.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_reasoning_echo_resolver_e2e.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_relay_session_segments.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_review_engine.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_sanitiser_escalation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_secret_scope.py` | behaviour | subset: `internal/config` TestParseEnv (quotes, escapes, inline comments), TestParseEnvBOMDoesNotMangleFirstKey. Profile-scope and multiplex cases dropped |
| `tests/agent/test_secret_scope_env_cache.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_serves_routed_profile_pin.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_session_affinity_header.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_soul_legacy_bot_protocol.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_ssl_transport_contracts.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_switch_model_context.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_switch_model_reapplies_config_context_pin.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_title_generator.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_tool_call_guardrail_runtime.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_turn_completion_explainer.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_vision_aware_preprocessing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_vision_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/computer_use/test_cua_no_overlay.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/computer_use/test_cua_perf_knobs.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/computer_use/test_cua_telemetry.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/computer_use/test_cua_wayland_env.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/conformance/test_profile_write_tripwire.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/cron/test_cron_manage_profile_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/cron/test_file_permissions.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/e2e/core/upgrade/git/test_hostile_post.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/e2e/core/upgrade/test_config_roundtrip_properties.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/e2e/core/upgrade/test_fresh_process_entrypoints.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/gateway/test_agent_cache.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_multiplex_secret_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_profile_prefix_misdelivery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_responses_history_cap.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_browser_control_artifacts.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_config_env_bridge_authority.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_control_socket.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_cron_ticks_every_profile.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_discord_plugin_setup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_discord_prompt_timeout_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_discord_roles_dm_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_display_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_gateway_platform_event_hook.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_host_attach_lifecycle.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_host_attach_standalone.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_host_gateway_lock_refusal.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_host_gateway_profile_env.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_housekeeping_profile_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_kanban_notifier_watcher_dispatch_gate.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_launch_profile_scope_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_adapter_registry.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_api_server_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_credential_isolation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_hot_serve.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_http_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_pairing_stores.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_process_memo_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_unserved_shared_ingress.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_persist_home_channel.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_platform_reconnect.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_profile_signature_during_connect.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_reasoning_config_per_model.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_retired_session_reset_notice.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_degraded_db_continuity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_shared_ingress_profile_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_shared_platform_boilerplate.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_slack_plugin_setup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_startup_agent_budget_log.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_startup_launch_profile_hooks_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_startup_model_context_warmup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_status_strict_identity_live.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_webhook_adapter.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_webhook_cron_trigger.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_whatsapp_reply_prefix.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/hermes_cli/test_actual_provider.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anon_auth_core.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anon_picker.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anon_upgrade.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anthropic_model_flow_stale_oauth.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anthropic_oauth_flow.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_anthropic_provider_persistence.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_api_key_providers.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_api_mode_aliases.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_apply_model_switch_result_context.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_aux_assignment_reasoning_effort.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_aux_config.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_azure_foundry_entra.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_backup_all_profiles.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_banner_distribution_label.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_banner_release_channel.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_bare_custom_picker_row.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_boot_convergence.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_busy_input_mode_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_canonical_custom_identity.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_clear_stale_base_url.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_custom_provider_vision.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_fallback_chain_hot_reload.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_first_message.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_first_run_setup.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_init.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_mcp_config_watch.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_partial_update_hint.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_pet_pane.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_provider_resolution.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_startup_model_cost_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cli_tools_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cmd_update_docker.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_codex_runtime_plugin_migration.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_commit_build_updates.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_completer_config_reads.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_config.py` | behaviour | subset: `internal/config` TestLoadOverridesDefaults, TestDeepMerge, TestLoadMissingAndEmptyYieldDefaults, TestLoadRejectsBadRoots. Migration, save and validation cases dropped |
| `tests/hermes_cli/test_config_backups.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_cache_signature.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_check_diagnostics.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_display_language.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_dotted_key_names.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_edit_seed.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_effective.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_env_expansion.py` | behaviour | subset: `internal/config` TestEnvExpansion, TestEnvExpansionLateEnvIsSeen. Cache-staleness cases dropped: Go Load has no cache |
| `tests/hermes_cli/test_config_env_ref_parity.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_env_refs.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_lkg_backup.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_lock_free_cache_hit.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_migration_45_connections.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_migration_48_sandbox_image.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_migration_49_vercel_image.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_migration_50_tirith.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_migration_nested_guards.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_platform_env_routing.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_provider_id_string.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_read_guard.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_rmw_lock.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_save_integrity.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_set_coercion.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_set_list_values.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_set_platforms_redirect.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_unversioned_migration.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_validation.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_config_yaml_comment_preservation.py` | dropped | config writer, migrations, diagnostics or caches: Stage 1 only reads config.yaml |
| `tests/hermes_cli/test_container_aware_cli.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_context_cache_switch_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_credential_lifecycle.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_cron_fire_dashboard.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_cron_run_scheduler_ownership.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_cross_profile_kill_refusal.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_custom_provider_context_length.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_custom_provider_extra_headers.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_custom_provider_normalize_no_mutate.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_custom_provider_tls.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_dashboard_admin_endpoints.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_dashboard_auth_prefix.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_dashboard_register.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_dashboard_unified_launch.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_dashboard_web_dist_validation.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_data_dir_suffix.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_default_interface_resolution.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_deleted_profile_tombstone.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_deprecated_cwd_warning.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_desktop_repo_discovery_config.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_destructive_slash_confirm_gate.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_doctor.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_doctor_channel_hygiene.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_doctor_live.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_dump_env_visibility.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_dump_terminal_backend.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_ensure_hermes_home_memo.py` | dropped | ensure_hermes_home memo is a Python perf memo; Go Init is idempotent and cheap |
| `tests/hermes_cli/test_ensure_hermes_home_uid.py` | dropped | uid/gid chown for managed containers is not ported |
| `tests/hermes_cli/test_env_custom_keys.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_deprecation_truthtable.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_export_line_lifecycle.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_export_prefix.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_load_cache.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_loader.py` | behaviour | subset: `internal/config` TestParseEnvBOMDoesNotMangleFirstKey, TestParseEnvLatin1Fallback, TestSecretsPrecedence, TestParseEnv (export, empty assignment). UTF-16/32 handling and managed/external-source cases not ported |
| `tests/hermes_cli/test_env_loader_op_bootstrap.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_loader_self_reference.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_sanitize_on_load.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_env_write_race.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_exit_summary_resume_hint.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fallback_cmd.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fast_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fast_serve_launch.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fireworks_provider.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fleet_matrix_down_state.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_fleet_matrix_self_restart_pending.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_focus_view.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_gateway.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_gateway_migrate_multiplex.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_gateway_multiplex_lifecycle.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_gateway_multiplex_status.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_gateway_served_guard_ownership.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_gemini_free_tier_setup_block.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_get_env_value_scope.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_gmi_provider.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_goal_judge_timeout_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_gui_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_history_commentary_display.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_home_directory_diagnostics.py` | behaviour | `internal/config` TestInitDiagnosesDanglingLinksWithoutCreatingTargets, TestInitLeavesOperatorOwnedLinkModes |
| `tests/hermes_cli/test_home_init_soul_symlink.py` | dropped | SOUL.md seeding is not in config; decided in the prompt-builder task 2.2 |
| `tests/hermes_cli/test_hooks_cli.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_indicator_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_install_bucket_separation.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_inventory.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_ipv6_url_authorities.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_iron_proxy_cli.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_cli_dispatch_passthrough.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_core_functionality.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_decompose.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_dispatch_claim_allowlist.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_dispatch_unknown_assignee_event.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_host_cap.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_kanban_review_lifecycle.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_local_models_routes.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_local_quickstart.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_local_reasoning_default.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_local_runtime.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_local_server_lifecycle.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_main_model_custom_provider_normalization.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_install_shapes.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_installs.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_cli_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_env.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_loaders.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_regression.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_surfacing.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_managed_scope_writeguard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_catalog.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_catalog_env_boundary.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_discovery_timing.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_probe_redaction.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_security.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mcp_startup.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_mem_trim.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_memory_setup.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_memory_setup_env_denylist.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_memory_status_env_hint.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_meta_prompt_cache.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_moa_preset_config_warnings.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_moa_preset_deletion.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_moa_set_models_preserves_extra_keys.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_alias_credentials.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_assignment_env_key_mirror.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_catalog.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_flow_pooled_credentials.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_picker_secret_scope.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_prefix_routing.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_provider_persistence.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_set_preserve_custom_endpoint.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_set_secret_copy.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_switch_configured_provider_routing.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_switch_custom_providers.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_switch_persist_default.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_switch_user_provider_slug_verbatim.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_model_validation.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_models.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_models_dev_preferred_merge.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_models_relay_base_url.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_non_ascii_credential.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_noninteractive_config_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_normalize_main_model_assignment.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_nous_subscription.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_old_updater_shims.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_ollama_cloud_auth.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_oneshot_fallback.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_oneshot_resume.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_personality_none.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_personality_single_owner.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_picker_key_cmd_discovery.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_pip_install_detection.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_platform_plugin_env_injection.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_api_hot_mount.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_aux_tasks_dashboard.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_plugin_auxiliary_tasks.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_call_mcp.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_historic_compat.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_ownership_ledger.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_packs.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugin_provider_picker_residue.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins_cmd.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins_cmd_activation_keys.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins_cmd_catalog.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins_hub_live_catalog.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_plugins_hub_perf_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_pooled_served_profile_backend_unscoped.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_post_setup_gating.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_post_update.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_post_update_expose_cli.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_process_identity_canonical_matchers.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_profile_clone_channels.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_delete_log_handlers.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_delete_service_name.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_describer.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_display_name.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_distribution.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_export_credentials.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_export_default_path.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_export_special_files.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_identity_purge_cmd.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_listing_cache.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_pm_portability.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_rename_checkpoints.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_rename_service.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profile_service_cleanup_scopes.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profiles.py` | behaviour | subset: `internal/config` TestNormalizeProfileName, TestValidateProfileName, TestProfileHome, TestProfileName. Create/delete/rename/export/alias/service cases dropped |
| `tests/hermes_cli/test_profiles_s6_hooks.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profiles_sidebar_failed_load.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_profiles_sidebar_scope.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_prompt_api_key.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_provider_config_validation.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_provider_precedence.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_read_raw_config_readonly.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_relay_plugin_cutover.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_resolve_ephemeral_system_prompt.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_resolve_provider_corrupt_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_resolve_turn_limit.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_resource_limits.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_runtime_provider_late_binding.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_runtime_provider_resolution.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_sandbox_image_switch.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_secret_source_bootstrap.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_security_advisories.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_send_cmd.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_session_vacuum_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_sessions_export_backup_round_trip.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_sessions_repair_profiles.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_set_config_value.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_model_provider.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_noninteractive.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_profile_answers.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_setup_profile_owner.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_setup_profile_returning_user.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_setup_provider_catalog.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_reconfigure.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_reset_backup.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_telemetry.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_setup_terminal.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_efficiency.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_engagement.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_install_run.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_model.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_reliability.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_send_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_send_wiring.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_signals.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_shared_metrics_update_stop_reasons.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_sibling_config_migration.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_source_channel_integration.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_source_check.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_source_launcher_publication.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_stale_root_module_bridge.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_startup_fast_guards.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_startup_model_routing.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_state_db_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_stepfun_picker_merge.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_steward_detection.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_terminal_breadcrumbs.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_terminal_bridge_profile_scope.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_terminal_menu_fallbacks.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_timeouts.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_tools_config.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_tools_config_post_setup.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_tui_resume_flow.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_tui_terminal_reset_on_exit.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_update_channel.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_channel_stable.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_command.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_config_clears_custom_fields.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_config_migration_on_current.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_contract.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_external_checkout.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_finish.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_hangup_protection.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_import_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_inventory.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_launchd_fleet_restart.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_parked_branch_guard.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_receipt.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_receipt_truthfulness.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_scoped_reconciliation.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_update_secret_import_lock.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_voice_wrapper.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_wake_word_config_schema.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_web_capability_gateway_route.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_multi_profile_scope.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_profile_delete_settlement.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_profile_soul_writes.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_profiles_model_scope.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_profiles_off_loop.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_read_coalescing.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_recommended_default_profile.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_routers_destructive_profile_scope.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_server.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_auto_archive_gateway_lock.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_config_offloop.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_cron_repeat.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_executor_isolation.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_gateway_topology.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_messaging_posture.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_messaging_profiles.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_server_profile_unification.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_server_session_search.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_skill_editor.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_skills_profiles.py` | dropped | profile management, services or multiplex: Stage 1 resolves one profile per process |
| `tests/hermes_cli/test_web_server_stt_lease.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_web_server_tts_lease.py` | dropped | dashboard, desktop or TUI backend: TypeScript frontends are out of scope |
| `tests/hermes_cli/test_whatsapp_onboarding.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_cli/test_xiaomi_provider.py` | dropped | hermes_cli feature outside Stage 1 (setup wizard, model picker, update, kanban, plugins, doctor, ...) |
| `tests/hermes_state/test_guest_durability_barriers.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/hermes_state/test_state_synchronous_pragma.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/monitoring/test_gateway_health_export.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/plugins/dashboard_auth/test_jwt_clock_skew_leeway.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/dashboard_auth/test_nous_provider.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/dashboard_auth/test_self_hosted_provider.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/image_gen/test_xai_provider.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/platforms/test_interactive_setup_reconfigure_gate.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/test_kanban_dashboard_plugin.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/plugins/test_plugins_manage_profile_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/pm/test_lazy_install_policy.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/secret_sources/test_error_remediation.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/test_hermes_logging.py` | dropped | Python logging setup; Go uses log/slog configured in cmd/hermes (task 4.1) |
| `tests/test_load_yaml_file_readonly.py` | behaviour | subset: `internal/config` TestLoadRejectsBadRoots, TestLoadMissingAndEmptyYieldDefaults |
| `tests/test_scratch_dir.py` | dropped | scratch-dir pruning is not config; not in Stage 1 |
| `tests/test_wal_reset_repair_hint.py` | dropped | SQLite WAL repair hints are not ported (D: modernc sqlite, no repair machinery) |
| `tests/tools/test_approval_config_readonly.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_approval_mode_parity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_bot_turn_lock.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_cdp_override.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_cloud_provider_cache.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_command_timeout_race.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_console.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_hardening.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_headed_mode.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_lightpanda.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_real_profile.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_snapshot_threshold.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_ssrf_local.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_use_cli.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_browser_use_harness.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_clarify_gateway.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_computer_use_capture_routing.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_connectors_catalog.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_cron_approval_mode.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_delegate_control_actions.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_delegate_cron_sync_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_discord_tool.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_docker_config_migrate.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_file_read_guards.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_file_sync_back.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_hook_output_spill.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_image_generation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_kanban_persisted_identity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_kanban_tools.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_kanban_toolset_opt_in.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_local_env_blocklist.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_managed_modal_environment.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_config_whitespace_warning.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_connect_secret_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_identity_header.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_image_content.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_multiplex_connection_keys.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_stderr_lifecycle.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_mcp_tool.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_model_tools.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_oneshot_completion_linger.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_permanent_allowlist_reconcile.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_process_registry.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_read_file_schema_gating.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_refresh_agent_mcp_tools.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_removed_backend_migration.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_session_search.py` | behaviour | sessiontools: TestDiscoverHydratesTopAndExcludesCurrent, TestDiscoverNoMatchAndBadBounds, TestScrollAndRead, TestReadCapsLongSessionsAndClipsMessages, TestBrowseListsRecentExcludingCurrent, TestArchivedRowsStaySearchableAndTokensAreRedacted, TestParseBound |
| `tests/tools/test_single_query_approval_mode.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_skillevaluator_scan.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_strict_provider_selection.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_subprocess_home_isolation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_terminal_config_env_sync.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_terminal_env_bridge.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tool_backend_helpers.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tool_output_limits.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tool_search.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tool_search_context_provider.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tool_search_multiquery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_transcription_dotenv_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_dotenv_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_macos_output.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_minimax_region.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_pythonpath_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_streaming.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_tts_xai_speech_tags.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_url_safety.py` | behaviour | netguard: TestBlockReason, TestCheckURL, TestClientRefusesLoopbackAtDialTime, TestClientRefusesRedirectToCredentialURL |
| `tests/tools/test_vercel_sandbox_environment.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_video_generation_tool_surface_matrix.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_vision_history_budget.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_vision_native_fast_path.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_vision_tools.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_voice_cli_integration.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_voice_client_config.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_voice_credential_pool_resolution.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_voice_mode.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_voice_stop_phrase.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_wake_word.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_web_free_fast_search.py` | dropped | multi-provider free/fast search routing: Stage 1 has one search backend |
| `tests/tools/test_web_keyless_fallback.py` | dropped | multi-provider keyless fallback: Stage 1 has one search backend |
| `tests/tools/test_web_managed_routing.py` | dropped | managed gateway routing is not in Stage 1 |
| `tests/tools/test_web_providers.py` | dropped | provider registry: Stage 1 has one search backend |
| `tests/tools/test_web_providers_searxng.py` | dropped | SearXNG provider is not in Stage 1 |
| `tests/tools/test_write_approval.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_x_search_tool.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_xai_http_credentials.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_xai_http_storage.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tui_gateway/test_change_watcher.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_compaction_status.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_compression_config_hot_reload.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_compute_host_routed_secret_hydration.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_eager_multiplex_activation.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_external_process_picker.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_free_tier_rpc.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_free_tier_task_done.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_goal_command.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_gui_surface_toolsets.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_heartbeat_tui_tick.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_inline_rpc_gil_starvation.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_kanban_resume_guard.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_launch_db_home_override_race.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_launch_profile_scope_plugin_hooks.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_local_model_session_identity.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_loop_command.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_moa_reference_emit.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_multi_profile_hosting_transitions.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_plugins_manage_late_activation.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_plugins_manage_settings.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_profiles_configure.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_profiles_describe_secret_scope.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_projects_rpc.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_reaper_flush_profile_scope.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_review_summary_callback.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_secret_capture_session.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_session_agent_cwd.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_session_control.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_session_interrupt_cancels_background_review.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_session_profile_db.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_stale_provider_resume_live.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_stranded_session_adoption.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_subagent_child_mirror.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_undo_command.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |
| `tests/tui_gateway/test_ws_keepalive_config.py` | dropped | TUI/desktop backend, ACP, computer use, PM, monitoring or conformance tooling: not in Stage 1 |

### session (375 files, floor 87.3%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/acp_adapter/test_empty_session_persistence.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/acp_adapter/test_session_cwd_column.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/acp_adapter/test_session_db_private_access.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/acp_adapter/test_session_provenance.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/agent/test_860_dedup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_api_content_row_addressed_backfill.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_async_token_accounting.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_background_review_cache_parity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_codex_app_server_persist.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_codex_app_server_thread_resume.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_codex_echo_ownership.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compaction_anti_thrash.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_abort_state_reset.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_adoption_preserves_live_tail.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_anti_thrash_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_anti_thrash_recovery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_boundary_hook.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_child_source.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_closed_adoption.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_commit_fence_race.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_count_warning.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_logging_session_context.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_orphan_recovery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_stall_interrupt.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_supersession_split.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_terminal_activity_persist.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_compression_worker_isolation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_context_compressor_bench_probe.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_context_engine_host_contract.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_corruption_recovery_guidance.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_cross_process_turn_lease.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_declared_conversation_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_empty_response_recovery_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_flush_diverts_on_corrupt_state_db.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_hygiene_timeout_cooldown_isolation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_identity_flush.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_idle_compaction_lock_and_guards.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_in_place_compaction.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_inline_session_search_profile.py` | dropped | cross-profile recall and inline @session links are not Stage 1 |
| `tests/agent/test_insights.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_live_db_preview_locks.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_message_reactions.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_message_sequence_repair.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_micro_compaction.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_notification_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_proactive_prune_restart_safety.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_prompt_cache_prefix_contract.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_prompt_cache_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_replay_cleanup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_rotation_flush_persisted_boundary.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_run_agent_codex_responses.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_session_rotation_flush_cold_resume.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_session_row_under_live_agent_persist.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_session_snapshot_removal.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_steer_compaction_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_stream_reasoning_details.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_studio_bridge_affinity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_synthetic_turn_display_kind.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_system_prompt_switch_invalidation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_token_guard_session_source.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_transform_llm_output_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_turn_finalizer_final_response_persistence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_usage_anchor.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_voice_turn_route.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/test_would_grow_refusal_runway.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/agent/transports/test_codex_event_projector.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/conformance/persistence/test_cell2_consume_once.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/cron/test_scheduler_completion_verification.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/e2e/core/live/test_live_providers.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/gateway/test_agent_cache_pressure.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_delegation_delivery_contract.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_active_work_drain.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_compaction_projection.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_api_server_declared_conversation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_async_session_db.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_auto_reset_clean_context.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_channel_continuity_hint.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_codex_hygiene_compaction.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_completion_delivery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_compression_concurrent_sessions.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_dedupe_user_turns.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_delete_session_no_resurrect.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_handoff_owned_recovery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_handoff_watcher_async_db.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_handoff_watcher_resilience.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_hosted_rooms.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_hygiene_failure_cooldown_ladder.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_hygiene_warning_lifecycle.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_load_transcript_db_only.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_multiplex_residue_parity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_peer_dm_hidden_e2e.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_peer_dm_into_open_bot_chat.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_peer_dm_transient_retry.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_profile_identity_purge.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_restart_drain_recovery_dedup.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_routing_save_fast_path.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_api.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_continuity.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_db_corrupt_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_db_replaced_fallback.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_db_warning_recheck.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_dm_thread_seeding.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_id_cache_coherence.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_store_expiry_finalized.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_session_store_stale_prune.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_shutdown_inflight_transcript_flush.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_slash_command_profile_scope.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_stale_self_heal_agent_cache_eviction.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_switch_session_keeps_model_override.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_telegram_topic_profile_isolation.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_timestamp_sidecar_replay.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_usage_command.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/gateway/test_wake_delivery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/hermes_cli/test_backup.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_branch_command.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_chat_c_fail_loudly.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_cli_insights_command.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_cli_interrupt_ack_race.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_cli_new_session.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_cli_retry.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_cli_shutdown_memory_messages.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_cli_yolo_resume_persistence.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_console_engine.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_dashboard_plugin_routes_secret_scope.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_doctor_structural_corruption.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_empty_session_hygiene.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_foreign_sessions.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_fts_optimize_notice.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_goals.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_handoff_slow_dispatch_timeout.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_kanban_resume_guard.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_kanban_worker_session_source.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_list_recent_user_messages_handoffs.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_loops.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_models_analytics_attribution.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_models_analytics_dedup.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_observational_sessiondb_modes.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_oneshot_resumed_session_persist.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_oneshot_usage_file.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_prune_spares_pinned.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_resolve_last_session.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_resume_display.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_resume_model_restore.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_export.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_export_batch.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_handoff.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_list_reader_disposable.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_listing.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_message_page_owner.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_messages_inline_images.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_owner_backfill.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_recovery.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_recovery_lost_and_found.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_recovery_model_config.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_recovery_timestamps.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_schema_history.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_search_context_batch.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_switch_mid_turn_guard.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_system_prompt_dedup.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_session_timeline.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_delete.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_error_exit_codes.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_export_md_cli.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_export_output_dir.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_export_retention.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_held_store_gate.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_list_truncation.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_pin.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_repair_check_only_exit.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_sessions_repair_prompts.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_single_query_session_finalize.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_status.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_subagent_notification_display.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_ux_messages_chat_sessions.py` | later | CLI command for a later task (4.1 chat, 5.2 agent_memory, 6.1 cron, 7.1 gateway) |
| `tests/hermes_cli/test_web_analytics_corrupt_store.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_plugins_catalog.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_routers_tools_install_on_enable.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_auto_archive_profile_config.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_files.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_messaging_stale_runtime.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_session_search_titles.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_sessions_compression.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_cli/test_web_server_update_status.py` | dropped | hermes_cli feature outside Stage 1 (sessions browser, repair, export, setup, update, ...) |
| `tests/hermes_state/test_append_message_after_close.py` | compiler | a closed *sql.DB returns an error by construction; no Python-style reopen/None-connection state exists in Go |
| `tests/hermes_state/test_append_messages_batch.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_auto_archived_lineage_reactivation.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_auto_vacuum_holder_gate.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_auto_vacuum_in_process_holder_gate.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_automatic_ended_stamp.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_aux_usage_accounting.py` | behaviour | subset: `internal/session` TestTitleAndUsage (AddUsage totals). Per-model usage table not ported |
| `tests/hermes_state/test_background_review_session_isolation.py` | later | re-bucket when the owning module ports |
| `tests/hermes_state/test_bounded_recent_sessions.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_canonical_title_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_clean_close_residual_poison.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_compacted_message_reactions.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_composite_carrier_rewind.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_compression_lease_refresh_before_publish.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_compression_lineage_guard.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_compression_watermark_commit.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_conversation_root.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_corrupt_row_robustness.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_dedupe_migration_contention.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_delegate_cascade.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_delegate_child_routing_inheritance.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_delete_compression_chain.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_delete_mode_read_busy_wait.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_delete_session_write_guards.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_deleted_wal_checkpoint_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_deleted_wal_generation_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_display_dedupe_pruned_tool_payload.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_display_projection_parity.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_display_split_heal.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_empty_sweep_archived_transcript.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_cjk_bigram.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_fresh_bootstrap_admission.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_index_fail_open.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_orphan_shadow_repair.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_projection_integrity.py` | behaviour | subset: `internal/session` TestSearchIndexFollowsEditsAndDeletes (view, triggers and index agree). Integrity checker and rebuild not ported |
| `tests/hermes_state/test_fts_rebuild_admission.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_rebuild_corruption_class.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_runtime_rebuild.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_tool_write_bounds.py` | behaviour | `internal/session` TestSearchToolRoleScansFullBody (indexed prefix bound; tool-role search scans the full body) |
| `tests/hermes_state/test_fts_trigram_cron_exclusion.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_trigram_subagent_exclusion.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_fts_update_of_narrowing.py` | behaviour | `internal/session` TestSearchIndexFollowsEditsAndDeletes (UPDATE OF trigger skips non-content writes) |
| `tests/hermes_state/test_get_anchored_view.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_get_messages_around.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_get_messages_include_compacted.py` | behaviour | subset: `internal/session` TestMessagesVisibility (display view adds archived, never rewound). Display-order dedupe not ported |
| `tests/hermes_state/test_hermes_state.py` | behaviour | subset: `internal/session` TestCreateAndGetSession, TestAppendMessageRoundTripAndCounters, TestMessagesOrderIsByIDNotTimestamp, TestMessagesPaging, TestEndSessionFirstReasonWinsAndReopen, TestSystemPromptIsStoredOnce, TestSearchRoutesAndOperators. Batch append, lineage, export, gateway fields and repair cases not ported |
| `tests/hermes_state/test_hermes_state_compression_busy_retry.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_hermes_state_compression_locks.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_hermes_state_readonly_preflight.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_hermes_state_ux_messages.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_hermes_state_wal_fallback.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_journal_mode_config.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_lease_pid_namespace.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_live_db_guard_ancestry.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_live_db_isolation_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_message_uid.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_named_profile_session_db.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_never_active_keyed_prune.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_no_more_rows_retry.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_orphan_gateway_session_repair.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_orphan_sweep_cross_backend.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_pinned_archived_sidebar.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_pinned_lineage.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_prune_activity_recency.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_purge_profile_state.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_read_path_transient_ioerr.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_readonly_fts_decode_error.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_reasoning_roundtrip.py` | behaviour | `internal/session` TestAppendMessageRoundTripAndCounters (reasoning, reasoning_content, reasoning_details) |
| `tests/hermes_state/test_rebuild_table_atomic.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_recent_model_route.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_rekey_profile_state.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_remove_session_files_glob_escape.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_repaired_history_commit_coverage.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_replace_messages_archive_siblings.py` | behaviour | internal/session TestReplaceLiveArchivesAndKeepsSessionID |
| `tests/hermes_state/test_replace_messages_archives_suffix_only.py` | behaviour | internal/session TestReplaceLiveArchivesAndKeepsSessionID, TestReplaceLiveIsAtomic |
| `tests/hermes_state/test_resolve_resume_session_id.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_restore_alternation_repair.py` | behaviour | internal/agent TestOutboundHealsBrokenPairs, TestCrashLeftToolTailIsClosedOnNextTurn, CheckAlternation |
| `tests/hermes_state/test_retired_wal_generation_capture.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_rewind_surfaces_invariant.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_schema_read_probe.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_search_or_relaxed_fallback.py` | behaviour | `internal/session` TestSearchORRelaxedFindsParaphrase, TestOrRelaxed |
| `tests/hermes_state/test_session_archived_round_trip.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_archiving.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_db_acp_cwd_backfill.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_db_context_manager.py` | compiler | Python with-block lifecycle; Go uses defer Close, which the compiler does not need a test for |
| `tests/hermes_state/test_session_db_leak_sweep.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_db_profile_stamp.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_db_read_conn_pool.py` | dropped | Python read-connection pool; database/sql owns pooling in Go |
| `tests/hermes_state/test_session_db_read_path_split.py` | dropped | Python read/write connection split; not ported |
| `tests/hermes_state/test_session_delete_profile_isolation.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_export_timings.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_git_metadata_generation.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_hidden.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_id_recognition_recovery.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_import_keep_flags.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_import_value_validation.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_lifecycle_status.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_list_index_choice.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_md_export.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_model_usage_pk_heal.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_read_state.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_session_turn_lease.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_session_workspace_binding.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_shared_session_db_registry.py` | dropped | process-wide shared SessionDB registry is a Python singleton; Go callers pass *Store |
| `tests/hermes_state/test_show_subagents_listing.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_sqlite_lock_safe_inspection.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_stale_tool_call_marker_session_repair.py` | behaviour | internal/agent outbound(): orphan results dropped, unanswered calls answered |
| `tests/hermes_state/test_startup_maintenance_lease.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_corrupt_quarantine.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_file_identity.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_fts_segment_collision_probe.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_lock_fail_closed.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_maintenance_conformance.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_malformed_repair.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_notadb_fail_closed.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_repair_live_writer_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_repair_loop_cap.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_repair_loop_mtime.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_repair_non_destructive.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_second_process_maintenance.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_stats.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_wal_unlink_race.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_state_db_write_durability.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_storage_health_latch.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_sweep_orphaned_sessions.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_tail_rewind_semantics.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_telegram_topic_selfheal.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_unaddressed_rewrite_identity.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_wal_active_confirmed.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_wal_checkpoint_strategy.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_wal_lock_guard.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_wal_lock_guard_lifecycle.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_watermark_interplay.py` | dropped | session lineage, lease, lock, watermark, rewind or profile-stamp machinery of the Python store is not ported: Go compaction replaces the live rows in one transaction under the same session id |
| `tests/hermes_state/test_write_lock_patience.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/hermes_state/test_writer_conn_thread_safety.py` | behaviour | `internal/session` TestConcurrentAppendsAreAllStored under -race (BEGIN IMMEDIATE) |
| `tests/hermes_state/test_zeroed_state_db.py` | dropped | storage repair, WAL/lock guards, FTS maintenance, display projection, archiving, export or gateway state: not carried over (modernc sqlite, one profile per process) |
| `tests/perf_guards/test_pattern_b_scaling.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/plugins/test_achievements_plugin.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/test_search_cjk_prefix_wildcard.py` | behaviour | `internal/session` TestSearchCJKUsesSubstringScan (trailing * stripped on the LIKE route) |
| `tests/tools/test_approval_yolo.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_bot_live_owner_delivery.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_bot_mode_canonical_chat_resolution.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_completed_process_results.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_connector_local_batches.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_kanban_provenance.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tools/test_skill_provenance_external.py` | later | belongs to a module ported by a later task; re-bucket there |
| `tests/tui_gateway/test_backend_heartbeat.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_bot_relay_methods.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_branch_system_prompt.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_composite_carrier_rewind.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_custom_provider_session_persistence.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_default_profile_session_name.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_deferred_model_history.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_desktop_session_login_identity.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_desktop_session_row_git_meta.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_desktop_session_workspace_persistence.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_finalize_session_persist.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_gateway_owned_session_reap.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_history_inline_images.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_lazy_session_regressions.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_out_of_band_history_adoption.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_pending_hidden_consumption.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profile_db_readonly.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profile_rebuild_commit.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profile_target_unavailable.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profiles_list_canonical_session.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profiles_list_session_field_cache.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_profiles_list_worker_session.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_queued_prompt_persistence.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_queued_prompt_restart_retire.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_resume_profile_scope.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_resume_readonly_mount.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_resume_session_yolo.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_resume_switched_provider_endpoint.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_routing_provenance_reopen.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_seeded_branch_title_routing.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_seeded_session_create.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_serve_exit_flush.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_archive_rpc.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_foreign.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_git_metadata_generation.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_hidden_rpc.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_history_codex_sidecar.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_list_lineage_rpc.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_session_resume_materializes_minted_key.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_ssh_session_cwd_persistence.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_startup_orphan_sweep.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_stream_interrupt_recovery_orphan.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_subagent_snapshot.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_submit_row_session_target.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_submit_time_user_row.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |
| `tests/tui_gateway/test_todo_state_events.py` | dropped | TUI/desktop backend, ACP, conformance, e2e or perf tooling for the Python stack: not in Stage 1 |

### skills (57 files, floor 83.1%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_external_skills.py` | behaviour | subset: `internal/skills` TestLoadSettingsRoots, TestCatalogLocalShadowsExternal |
| `tests/agent/test_external_skills_dirs_cache.py` | dropped | external-dirs cache is a Python perf memo; Go scans on demand |
| `tests/agent/test_phantom_tool_references.py` | behaviour | ported: prompttext ExecutionGuidanceFor drops lines for absent tools (execution_guidance_golden, systemprompt golden execution_filtered_by_tools) |
| `tests/agent/test_project_skills.py` | dropped | project-local (trust-gated) skills are not ported in Stage 1 |
| `tests/agent/test_prompt_cache_boundary.py` | behaviour | ported: cache_test.go stable-prefix and breakpoint cases (274 golden cases) |
| `tests/agent/test_read_dedup_rewrite_boundaries.py` | later | read de-duplication is a file-tool concern: re-bucket in task 3.3 |
| `tests/agent/test_skill_applied_notifications.py` | later | CLI notification: re-bucket in task 4.1 |
| `tests/agent/test_skill_bundles.py` | dropped | skill bundles are not ported in Stage 1 |
| `tests/agent/test_skill_cache_thrash.py` | dropped | skill scan cache and its invalidation are not ported |
| `tests/agent/test_skill_commands.py` | behaviour | subset: `internal/skills` TestSlugifyName, TestCommands, TestCommandsDuplicateNamesRegisterOnce. Message building, preprocessing and bundles are not in this package (task 2.3/4.1) |
| `tests/agent/test_skill_commands_reload.py` | dropped | reloading skills mid-session breaks the prompt-cache invariant; the index is built once per session |
| `tests/agent/test_skill_inline_shell_trust.py` | dropped | inline shell expansion in skills is not ported in Stage 1 |
| `tests/agent/test_skill_invocation_description.py` | later | slash-command invocation text: re-bucket in task 4.1 |
| `tests/agent/test_skill_session_platform_gate.py` | behaviour | ported: skillsindex golden cases session_platform_match, _mismatch and _unknown_fails_open |
| `tests/agent/test_skill_utils.py` | behaviour | subset: `internal/skills` TestParseFrontmatterBOMMatchesPlain, TestIndexFilesPrunesExcludedAndSupportDirs, TestParseConfigStringList, TestLoadSettingsDisabled. Subprocess-home config vars not ported |
| `tests/agent/test_skills_auto_load.py` | dropped | skills.auto_load is not ported (decision G17) |
| `tests/gateway/test_skill_slash_native_boundary.py` | later | gateway skill handling: re-bucket in task 7.1 |
| `tests/gateway/test_stacked_skill_platform_disabled.py` | later | gateway skill handling: re-bucket in task 7.1 |
| `tests/gateway/test_unavailable_skill_hint.py` | later | gateway skill handling: re-bucket in task 7.1 |
| `tests/hermes_cli/test_banner_skills.py` | dropped | Python CLI banner |
| `tests/hermes_cli/test_cli_reload_skills.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_cli_secret_capture.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_commands.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_discord_skill_clamp_warning.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_oneshot_skills.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_session_skill_previews.py` | dropped | dashboard session previews |
| `tests/hermes_cli/test_skill_collision_visibility.py` | later | CLI skill commands, reload, preload or secret capture: re-bucket in task 4.1 (Discord clamp in 7.3) |
| `tests/hermes_cli/test_skills_config.py` | dropped | skills config UI/CLI (writer) is not ported |
| `tests/hermes_cli/test_skills_hub.py` | dropped | skills hub (browse/install from registries) is not in Stage 1 |
| `tests/hermes_cli/test_update_modified_notice.py` | dropped | hermes update flow is not ported |
| `tests/hermes_platform/test_declaration.py` | dropped | app declarations are not ported: a skill with requires_apps is hidden (see `internal/skills` TestMatchesApps) |
| `tests/skills/test_google_workspace_credential_files.py` | dropped | tests a specific skill's own Python script, not the loader |
| `tests/skills/test_property_listings_skill.py` | dropped | tests a specific skill's own Python script, not the loader |
| `tests/skills/test_skill_document_contracts.py` | behaviour | subset: `internal/skills` TestRepoSkillsLoadWithoutError (every repo SKILL.md parses with frontmatter) |
| `tests/tools/test_cross_profile_guard.py` | dropped | cross-profile guard for multiplexed profiles is not ported |
| `tests/tools/test_plugin_skills.py` | dropped | plugin-namespaced skills are not ported (no plugin system in Stage 1) |
| `tests/tools/test_skill_create_dir.py` | dropped | skills.create_dir tier is not ported |
| `tests/tools/test_skill_env_passthrough.py` | dropped | skill env passthrough to remote backends is not in Stage 1 |
| `tests/tools/test_skill_improvements.py` | behaviour | TestPatchAndEdit (patch keeps frontmatter valid, edit keeps over-budget descriptions maintainable), TestCreateRejections |
| `tests/tools/test_skill_ledger.py` | dropped | skill provenance ledger is not ported |
| `tests/tools/test_skill_manage_batch.py` | dropped | batch operations array is not ported: skill_manage takes one flat operation |
| `tests/tools/test_skill_manage_schema_diet.py` | dropped | the operations-array schema is not ported (flat schema) |
| `tests/tools/test_skill_manager_tool.py` | behaviour | TestCreateViewListRoundTrip, TestCreateRejections, TestCreateDuplicate, TestPatchAndEdit, TestSupportFiles, TestDeleteRemovesEmptyCategory, TestUnknownAction |
| `tests/tools/test_skill_pending_diff_batch.py` | dropped | pending-diff review of skill edits is not ported |
| `tests/tools/test_skill_size_limits.py` | behaviour | TestCreateRejections (oversize, description budget) |
| `tests/tools/test_skill_view_dedup.py` | dropped | repeat-view stub dedup is not ported |
| `tests/tools/test_skill_view_traversal.py` | behaviour | TestSupportFiles (traversal and absolute paths refused for view and write), FuzzValidateFilePath |
| `tests/tools/test_skills_sync.py` | dropped | bundled-skill sync into the profile is Python install machinery |
| `tests/tools/test_skills_tool.py` | behaviour | TestCreateViewListRoundTrip, TestDisabledSkillHiddenFromListButViewable, TestSupportFiles |
| `tests/tools/test_skills_tool_discovery_cache.py` | dropped | skill discovery cache is not ported |
| `tests/tools/test_skills_tool_profile_scope.py` | dropped | profile scoping is the loader seam: skilltools.Loader takes the call home (TestCreateViewListRoundTrip) |
| `tests/tui_gateway/test_command_description_fidelity.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |
| `tests/tui_gateway/test_human_input_hooks_tui.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |
| `tests/tui_gateway/test_plugin_skill_slash.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |
| `tests/tui_gateway/test_protocol.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |
| `tests/tui_gateway/test_sudo_command_context.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |
| `tests/tui_gateway/test_tui_gateway_server.py` | dropped | TUI/desktop backend: TypeScript frontends are out of scope |

### anthropic (108 files, floor 83.8%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_account_policy_block.py` | behaviour | `internal/provider/anthropic` providerSpecial's account-policy rule (covered in the classifier; no Anthropic-shaped corpus case, unit text in classify.go) |
| `tests/agent/test_anthropic_adapter.py` | behaviour | subset: streaming/response normalisation (2.1c, TestStreamMatchesPythonSDK), request building (2.1b, TestBuildRequestMatchesPython, TestSupportsFastModeMatchesPython), `internal/provider/anthropic` golden tests (TestConvertMessagesMatchesPython, fixtures from the Python converter), TestConvertToolsMatchesPython, TestModelHelpersMatchPython. Request kwargs (2.1b), response normalisation (2.1c) and credentials (2.1d) re-bucket there |
| `tests/agent/test_anthropic_borrowed_row_authority.py` | dropped | credential-pool rows are not ported |
| `tests/agent/test_anthropic_client_auth.py` | behaviour | subset: `internal/provider/anthropic` TestApplyHeaders, TestTokenResolutionOrder, TestCredentialsWorkWithTheClient (API key vs OAuth bearer and identity). Custom/third-party/Bedrock endpoint styles are dropped |
| `tests/agent/test_anthropic_credential_persist_failure.py` | behaviour | `internal/provider/anthropic` TestRefreshLoginFailsClosedWhenTheCommitFails, TestWriteFileCreatesAndFailsClosedOnCorruption, TestWriteFileKeepsSiblingsScopesAndMode |
| `tests/agent/test_anthropic_dead_grant_hints.py` | behaviour | subset: `internal/provider/anthropic` TestOAuthErrorClassification, TestRefreshLoginRemembersADeadGrant, TestExpiredLoginRefreshFailureIsReported. The `hermes auth add` hint wording is CLI text (task 4.1) |
| `tests/agent/test_anthropic_external_login_optout.py` | behaviour | `internal/provider/anthropic` TestTokenResolutionOrder (adoption off by default: stricter than Python, which adopts by default) |
| `tests/agent/test_anthropic_keychain.py` | behaviour | `internal/provider/anthropic` TestKeychainFindParsesBothAttributeEncodings, TestKeychainFindReturnsNothingForUnusableOutput, TestStoreReadSources, TestMirrorToKeychainOnlyUpdatesTheItemHoldingTheSpentPair, TestMirrorCommandKeepsTheSecretOffArgvAndHexEncoded, TestRefreshLoginAdoptsAnAlreadyRotatedTokenWithoutPosting, TestConcurrentRefreshesSpendTheSingleUseTokenOnce; all with a fake Keychain, none touch the real one |
| `tests/agent/test_anthropic_kimi_signed_thinking_replay.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_anthropic_kwargs_sanitize.py` | compiler | Python strips Responses-only kwargs that leaked into a Messages call (api_mode race); Go's typed `Request` cannot carry them |
| `tests/agent/test_anthropic_mcp_prefix_strip.py` | behaviour | `internal/provider/anthropic` TestUnprefixOAuthToolName (registry lookup, aliases last, only with StripToolPrefix) |
| `tests/agent/test_anthropic_message_stop.py` | behaviour | `internal/provider/anthropic` TestStreamMatchesPythonSDK (23 SSE scenarios replayed through the real Python SDK and `normalize_response`) and TestStreamEndingWithoutMessageStopIsIncomplete |
| `tests/agent/test_anthropic_model_cooldowns.py` | dropped | per-(credential, model) cooldowns belong to the credential pool, which is not ported |
| `tests/agent/test_anthropic_oauth_pkce.py` | dropped | Hermes's own PKCE login (`hermes auth add anthropic`) is not ported; Stage 1 borrows Claude Code's login or takes a key |
| `tests/agent/test_anthropic_oauth_stress.py` | behaviour | `internal/provider/anthropic` TestConcurrentRefreshesSpendTheSingleUseTokenOnce (-race), TestLockFileTimesOutAndHonoursContext |
| `tests/agent/test_anthropic_oauth_ua_prefix.py` | behaviour | `internal/provider/anthropic` TestApplyHeaders (claude-code/<version> (external, cli) for inference, axios for the token endpoint in TestExchangeSendsTheRefreshGrant) |
| `tests/agent/test_anthropic_output_field_leak.py` | behaviour | `internal/provider/anthropic` golden tests (TestConvertMessagesMatchesPython, fixtures from the Python converter) ordered_blocks_replay (output-only fields such as parsed_output, caller, citations=None are whitelisted away) |
| `tests/agent/test_anthropic_owned_routing.py` | dropped | credential-pool owned routing is not ported |
| `tests/agent/test_anthropic_request_blank_block_guard.py` | behaviour | `internal/provider/anthropic` golden tests (TestConvertMessagesMatchesPython, fixtures from the Python converter) blank_everything, plus FuzzConvertMessagesInvariants (no blank text block ever reaches the wire) |
| `tests/agent/test_anthropic_request_client_reuse.py` | dropped | SDK client reuse: the Go client is a plain struct over net/http with a shared transport |
| `tests/agent/test_anthropic_route_oauth_identity.py` | dropped | OAuth identity per custom/third-party base URL: Stage 1 is the native endpoint only, where the credential shape decides |
| `tests/agent/test_anthropic_spent_rotation_verdict.py` | behaviour | `internal/provider/anthropic` TestSpentRotationRegistry, TestSpentRotationInMemoryIsBounded, TestRefreshLoginPreconditions, TestSpentAccessTokenIsRefused |
| `tests/agent/test_anthropic_stream_fallbacks.py` | behaviour | `internal/provider/anthropic` TestStreamFallsBackToCreateWhenStreamingIsUnavailable, TestStreamFallsBackOnUnexpectedEventOrder, TestOtherStreamFailuresDoNotFallBack, TestEventStreamAnswerToAPlainCreateIsAggregated |
| `tests/agent/test_anthropic_structured_output.py` | dropped | structured-output (JSON schema output_config) is not used in Stage 1 |
| `tests/agent/test_anthropic_thinking_block_order.py` | behaviour | `internal/provider/anthropic` golden tests (TestConvertMessagesMatchesPython, fixtures from the Python converter) ordered_blocks_replay, ordered_blocks_nothing_cacheable, thinking_* cases |
| `tests/agent/test_anthropic_thinking_disable.py` | behaviour | `internal/provider/anthropic` TestBuildRequestMatchesPython (288 cases generated by running Python `build_anthropic_kwargs`): reasoning enabled=false per model family (disabled / between_tools / omitted) |
| `tests/agent/test_anthropic_third_party_oauth_guard.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_anthropic_token_scope_isolation.py` | dropped | multiplexed-profile secret scope is not ported |
| `tests/agent/test_anthropic_whitespace_text_blocks.py` | behaviour | `internal/provider/anthropic` golden tests (TestConvertMessagesMatchesPython, fixtures from the Python converter) blank_everything / system_blocks_cached, FuzzConvertMessagesInvariants |
| `tests/agent/test_auth_provider_failover.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_aux_stream_host_deadline_sibling_wires.py` | behaviour | subset: `internal/provider/anthropic` TestOnEventErrorAbandonsTheStream, TestContextCancelStopsTheCall (the caller's deadline seam). Sibling non-Anthropic wires are dropped |
| `tests/agent/test_auxiliary_anthropic_pool_fallback_regression.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_client.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_client_anthropic_custom.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_client_base_url_host_validation.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_client_cache_follows_credential.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_client_minimax_oauth.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_auxiliary_error_classification.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_auxiliary_explicit_base_anthropic.py` | dropped | the auxiliary client and its routing are not ported: the summarizer is a plain call through the session's own provider (ProviderSummarizer) |
| `tests/agent/test_bedrock_1m_context.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_bedrock_guardrail_claude_route.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_bedrock_integration.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_billing_unverified_carrythrough.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_claude_code_version_path.py` | behaviour | `internal/provider/anthropic` TestDetectClaudeCodeVersion (PATH before well-known prefixes, non-numeric output skipped, static fallback) |
| `tests/agent/test_codex_model_entitlement_rotation.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_codex_reasoning_only_streak.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_codex_xai_oauth_recovery.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_compression_lock_defer.py` | dropped | cross-process compression locks; one turn at a time per session |
| `tests/agent/test_compression_timeout_terminal.py` | behaviour | TestSummarizerFailureAbortsWithEscalatingCooldown: a failed or timed-out summary aborts and arms the cooldown ladder |
| `tests/agent/test_content_policy_blocked.py` | behaviour | `internal/provider/anthropic` TestClassifyMatchesPython 400_safety_block, 400_cyber, 400_content_filter, stream_error_content (deterministic per request: fall back, never retry unchanged) |
| `tests/agent/test_context_token_tracking.py` | behaviour | TestShouldCompressThresholdAndCooldown, TestCompactionOnLongRecordedSession (reported prompt size, estimate when unknown) |
| `tests/agent/test_credential_pool_anthropic_refresh_race.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_auto_detect.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_interrupt.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_oat_authtype.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_oauth_writethrough.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_routing.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credential_pool_terminal_refresh_visibility.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_credit_404_fallback.py` | behaviour | `internal/provider/anthropic` TestClassifyMatchesPython 404_billing_code, 404_credit (credit exhaustion surfacing as 404 is billing, not model_not_found) |
| `tests/agent/test_ctx_halving_fix.py` | behaviour | ported: TestContextOverflowCompressesAndRetries, TestOverflowGivesUpAfterRepeatedCompressions (context-length probing itself is not ported; overflow compresses) |
| `tests/agent/test_custom_pool_mismatch_guard.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_deepseek_anthropic_thinking.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_entitlement_fail_closed.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_error_classifier_observed.py` | behaviour | subset: `internal/provider/anthropic` TestClassifyMatchesPython (185 cases: genuine `anthropic` SDK exceptions classified by Python's real `classify_api_error`). Provider-specific observed shapes (Codex, xAI, Nous, OpenRouter, Gemini) are dropped |
| `tests/agent/test_error_classifier_upstream_blocked.py` | behaviour | `internal/provider/anthropic` TestClassifyMatchesPython 403_cf and 403_blocked (WAF/CDN blocks stay upstream_blocked, not auth) |
| `tests/agent/test_fallback_api_mode_preservation.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_fallback_entry_base_url.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_fallback_exhaustion_cooldown.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_free_tier_rate_limit_class.py` | dropped | Nous free-tier fairshare 429 classification is not ported |
| `tests/agent/test_image_corrupt_recovery.py` | dropped | image input and its recovery are not in Stage 1 |
| `tests/agent/test_injected_param_strip_retry_registry.py` | dropped | strip-and-retry of injected params is a loop recovery for gateways that inject fields: not on the native route |
| `tests/agent/test_kimi_coding_anthropic_thinking.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_meta_agent_init.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_minimax_provider.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_moa_aggregator_prefix_stability.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_moa_alternation_recovery.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_multimodal_tool_content_recovery.py` | dropped | multimodal tool results are not in Stage 1 |
| `tests/agent/test_non_chat_primary_restore.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_none_deref_guards.py` | compiler | nil handling that Go's types and zero values rule out |
| `tests/agent/test_nonretryable_result_carries_verdict.py` | behaviour | ported: TestFatalErrorIsNotRetried, TestRetryBudgetThenFailure (TurnResult.Partial and Error carry the verdict) |
| `tests/agent/test_nous_portal_anthropic_wire.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_overflow_overhead_aware_tokens.py` | dropped | provider overhead accounting; overflow errors compress and retry (TestContextOverflowCompressesAndRetries) |
| `tests/agent/test_primary_runtime_restore.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_private_credential_writers.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_provider_fallback.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_provider_profile_recovery_seam.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_pydantic_dump_warning_leak.py` | dropped | Python SDK/pydantic model_dump warning; the Go client has no SDK objects |
| `tests/agent/test_retry_delay_parsers_shared.py` | behaviour | `internal/provider/anthropic` TestRetryHelpersMatchPython (the reset-window grammars, explicit retry-after beating resets-in) |
| `tests/agent/test_retry_utils.py` | behaviour | subset: `internal/provider/anthropic` TestRetryHelpersMatchPython, TestParseRetryAfterHTTPDate, TestProviderRetryAfter, TestBackoffDelay, TestRetryWait. The Z.AI Coding adaptive backoff is dropped |
| `tests/agent/test_run_agent.py` | behaviour | ported: internal/agent loop_test.go and loop_anthropic_test.go (tool rounds, retries, truncation, interrupts, budget) |
| `tests/agent/test_streaming.py` | behaviour | subset: `internal/provider/anthropic` TestStreamMatchesPythonSDK (23 SSE scenarios replayed through the real Python SDK and `normalize_response`), TestMalformedToolInputIsATypedError, TestNullUsageIsTolerated, TestOnEventSeesEveryEventAndCanAbort. Stream-delta display and the stale-stream watchdog re-bucket in task 2.3 |
| `tests/agent/test_switch_model_fallback_prune.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_switch_model_pool_reload.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_switch_model_rollback.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/agent/test_thinking_timeout_guidance.py` | dropped | stale-stream guidance copy for a Python-only watchdog |
| `tests/agent/transports/test_transport.py` | behaviour | subset: `internal/provider/anthropic` TestStreamMatchesPythonSDK (23 SSE scenarios replayed through the real Python SDK and `normalize_response`) (normalize_response, finish-reason map, validate_response). Other transports are dropped |
| `tests/gateway/test_output_caps_removed.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/hermes_cli/test_anthropic_pool_model_discovery.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/hermes_cli/test_auth_commands.py` | later | CLI auth commands: re-bucket in task 4.1 |
| `tests/hermes_cli/test_claude_code_account_connection.py` | dropped | `hermes` account-connection UI flow is not ported |
| `tests/hermes_cli/test_external_process_cli_prefixes.py` | dropped | external-process providers are not ported |
| `tests/hermes_cli/test_moa_config.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/hermes_cli/test_model_switch_opencode_anthropic.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/plugins/model_providers/test_commandcode_profile.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/plugins/test_langfuse_plugin.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/plugins/test_transform_api_error_classification_hook.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/tools/test_computer_use.py` | dropped | other providers/routes, the credential pool, multi-provider failover, MoA or plugins: Stage 1 is Anthropic-only with one credential |
| `tests/tools/test_delegate_fallback_matrix.py` | later | delegate tool: re-bucket in task 3.5 |

### prompt (39 files, floor 81.8%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_bot_profile_prompt_isolation.py` | dropped | Bot Mode is not ported |
| `tests/agent/test_compaction_prompt_rebuild.py` | behaviour | TestCompactionOnLongRecordedSession: the system prompt is rebuilt once at compaction with the note, and not again on resume |
| `tests/agent/test_context_breakdown.py` | dropped | context breakdown display (dashboard/TUI) |
| `tests/agent/test_context_display_provenance.py` | dropped | context display for the dashboard/TUI |
| `tests/agent/test_context_file_sources.py` | behaviour | ported: internal/agent/contextfiles_test.go, 48 golden cases from Python |
| `tests/agent/test_failover_identity.py` | dropped | multi-provider failover identity; Stage 1 is Anthropic only |
| `tests/agent/test_iteration_budget_race.py` | behaviour | `internal/agent` TestIterationBudget, TestIterationBudgetIsSafeUnderConcurrency (exact cap across goroutines, -race), TestNormalizeBudgetWarningRatio |
| `tests/agent/test_litellm_tool_part_cache_markers.py` | dropped | LiteLLM-style envelope route: Stage 1 is the native route, where tool results take a top-level marker |
| `tests/agent/test_moa_loop_mode.py` | dropped | MoA is not ported |
| `tests/agent/test_oneshot_footprint.py` | dropped | oneshot footprint accounting is not in Stage 1 |
| `tests/agent/test_platform_hint_cron_delivery.py` | behaviour | ported: internal/agent/systemprompt_test.go, 68 golden cases from Python plus the write-once and acceptance tests |
| `tests/agent/test_platform_hint_desktop.py` | dropped | desktop platform hint: the desktop app is out of scope |
| `tests/agent/test_platform_hint_overrides.py` | behaviour | ported: internal/agent/systemprompt_test.go, 68 golden cases from Python plus the write-once and acceptance tests |
| `tests/agent/test_plugin_prompt_sections.py` | dropped | plugin prompt sections: there is no plugin system in Stage 1 |
| `tests/agent/test_profile_home_override_precedence.py` | compiler | Home is an explicit option; context override precedence covered by internal/config home tests |
| `tests/agent/test_prompt_cache_ttl_auto.py` | behaviour | `internal/agent` TestTTLPolicy (auto resolves to 5m for machine-paced sources, 1h for human-paced; effective TTL clamp). Provider-specific clamps for Qwen/Alibaba are dropped |
| `tests/agent/test_prompt_caching.py` | behaviour | subset: `internal/agent` TestCachePlanMatchesPython (274 cases from Python's real `build_prompt_cache_plan`: both layouts, idempotent re-planning, no input mutation), TestPlanNeverExceedsFourBreakpoints, TestCacheMarkersShareOneTTL, FuzzPlanInvariants. Envelope/OpenRouter/LiteLLM layouts are dropped |
| `tests/agent/test_protected_tail_pressure.py` | behaviour | TestOldToolResultsBecomeStubs, TestPendingToolRoundIsNeverPruned |
| `tests/agent/test_skill_app_snapshot.py` | dropped | the disk snapshot and prompt cache are not ported (the index is built once per session); the apps gate is covered by the requires_apps_hides golden |
| `tests/agent/test_skills_guidance_content_filter.py` | behaviour | ported: SKILLS_GUIDANCE only with skill_manage, text verbatim from Python (systemprompt golden) |
| `tests/agent/test_soul_md_profile_isolation.py` | behaviour | ported: LoadSoulMD reads Home only (Home is explicit, no env fallback); contextfiles_test.go |
| `tests/agent/test_subdirectory_hints.py` | later | hints come from file-tool results: re-bucket in task 3.3 |
| `tests/agent/test_system_prompt.py` | behaviour | ported: internal/agent/systemprompt_test.go, 68 golden cases from Python plus the write-once and acceptance tests |
| `tests/agent/test_system_prompt_restore.py` | behaviour | ported: EnsureSystemPrompt reuses stored bytes on resume (TestEnsureSystemPromptWritesOnce, TestPromptCacheAcceptance) |
| `tests/agent/test_tool_batch_segmentation.py` | behaviour | ported: TestToolCallsRunInParallelWhenSafe, TestUnsafeToolsRunSequentially |
| `tests/agent/test_transcript_decoration_idempotence.py` | behaviour | `internal/agent` TestCachePlanMatchesPython (274 cases from Python's real `build_prompt_cache_plan`: both layouts, idempotent re-planning, no input mutation) (the re-plan half), TestStripCacheControlRestoresStringContent, FuzzPlanInvariants (idempotence) |
| `tests/agent/test_turn_base_display_anchor.py` | dropped | UI display anchoring, not loop behaviour |
| `tests/gateway/test_cached_agent_max_iterations.py` | later | gateway agent cache and iteration cap: re-bucket in task 7.1 |
| `tests/gateway/test_hosted_room_discussion.py` | later | re-bucket when the owning module ports |
| `tests/gateway/test_hygiene_compaction_keeps_seeded_prompt.py` | dropped | gateway pre-agent hygiene compaction is not ported |
| `tests/gateway/test_startup_environment_probe.py` | dropped | the environment probe and runtime hints are not ported |
| `tests/hermes_cli/test_cli_interrupt_subagent.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |
| `tests/hermes_cli/test_prompt_size.py` | dropped | the `hermes prompt-size` diagnostic is not in Stage 1 |
| `tests/hermes_cli/test_sessions_rest_scaffold_notices.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |
| `tests/tools/test_delegate_child_cache_ttl.py` | later | delegate children's cache TTL: re-bucket in task 3.5 |
| `tests/tools/test_docker_network_config.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |
| `tests/tools/test_translate_execute_context.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |
| `tests/tui_gateway/test_hud_surface_note.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |
| `tests/tui_gateway/test_multi_profile_hosting_fail_closed.py` | dropped | TUI/dashboard/CLI display, hosted rooms, docker or code-execution translation: not in Stage 1 |

### loop (116 files, floor 74.5%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/acp_adapter/test_failed_turn_closure.py` | | |
| `tests/agent/test_413_compression.py` | | |
| `tests/agent/test_anthropic_billing_guidance.py` | | |
| `tests/agent/test_anthropic_preserved_thinking_replay.py` | | |
| `tests/agent/test_authorization_gate.py` | | |
| `tests/agent/test_background_review.py` | | |
| `tests/agent/test_bedrock_context_cache.py` | | |
| `tests/agent/test_bot_chat_toolset_refresh.py` | | |
| `tests/agent/test_canon_args_memo_parity.py` | | |
| `tests/agent/test_codex_app_server_event_bridge.py` | | |
| `tests/agent/test_codex_incomplete_budget_escalation.py` | | |
| `tests/agent/test_codex_soft_failure_pool_rotation.py` | | |
| `tests/agent/test_codex_token_expired_replay_recovery.py` | | |
| `tests/agent/test_compression_budget_rearm.py` | | |
| `tests/agent/test_compression_budget_refund.py` | | |
| `tests/agent/test_compression_progress.py` | | |
| `tests/agent/test_content_policy_refusal_stop_details.py` | | |
| `tests/agent/test_context_engine_on_turn_complete_usage.py` | | |
| `tests/agent/test_context_engine_select_context.py` | | |
| `tests/agent/test_continuation_ceiling_wedge.py` | | |
| `tests/agent/test_conversation_loop_interpreter_shutdown.py` | | |
| `tests/agent/test_degenerate_final_recovery.py` | | |
| `tests/agent/test_detached_fork_lifecycle_hooks.py` | | |
| `tests/agent/test_empty_response_fallback_refund.py` | | |
| `tests/agent/test_engine_preflight_wire.py` | | |
| `tests/agent/test_error_classifier.py` | | |
| `tests/agent/test_export_current_turn_boundary.py` | | |
| `tests/agent/test_external_process_provider_init.py` | | |
| `tests/agent/test_failed_turn_chat_copy.py` | | |
| `tests/agent/test_failed_turn_site_codes.py` | | |
| `tests/agent/test_future_timeout_worker_death.py` | | |
| `tests/agent/test_gateway_turn_sidecar.py` | | |
| `tests/agent/test_gemini_fast_fallback.py` | | |
| `tests/agent/test_idle_compaction.py` | | |
| `tests/agent/test_image_rejection_fallback.py` | | |
| `tests/agent/test_image_shrink_recovery.py` | | |
| `tests/agent/test_in_place_merged_user_rows.py` | | |
| `tests/agent/test_infinite_compaction_loop.py` | | |
| `tests/agent/test_interrupt_issuer_attribution.py` | | |
| `tests/agent/test_iteration_budget_warning.py` | | |
| `tests/agent/test_kanban_budget_checkpoint.py` | | |
| `tests/agent/test_malformed_tool_args_no_fallback.py` | | |
| `tests/agent/test_max_iteration_handoff.py` | | |
| `tests/agent/test_mid_turn_compaction_turn_boundary.py` | | |
| `tests/agent/test_moa_observability_bridge.py` | | |
| `tests/agent/test_moa_prepared_request_client_swap.py` | | |
| `tests/agent/test_native_preflight_estimate.py` | | |
| `tests/agent/test_nous_rate_guard.py` | | |
| `tests/agent/test_nous_wire_auto.py` | | |
| `tests/agent/test_outbound_stale_vision.py` | | |
| `tests/agent/test_over_window_compression_fail_closed.py` | | |
| `tests/agent/test_overflow_partial_terminal.py` | | |
| `tests/agent/test_owned_process_cleanup.py` | | |
| `tests/agent/test_partial_stream_continuation_prompt.py` | | |
| `tests/agent/test_partial_stream_finish_reason.py` | | |
| `tests/agent/test_preflight_compression_gate.py` | | |
| `tests/agent/test_preflight_compression_timeout_fail_closed.py` | | |
| `tests/agent/test_proactive_prune_loop_wiring.py` | | |
| `tests/agent/test_provider_id_mint_normalization.py` | | |
| `tests/agent/test_rate_limit_reset_hint.py` | | |
| `tests/agent/test_reasoning_disable_recovery.py` | | |
| `tests/agent/test_recovery_diagnostic_producers.py` | | |
| `tests/agent/test_reference_handoff_active_turn.py` | | |
| `tests/agent/test_relay_atof_cwd.py` | | |
| `tests/agent/test_repair_reanchors_current_turn_user_idx.py` | | |
| `tests/agent/test_replay_budget_accounting.py` | | |
| `tests/agent/test_replay_echo_retirement.py` | | |
| `tests/agent/test_retry_exhaustion_partial_retention.py` | | |
| `tests/agent/test_run_budget.py` | | |
| `tests/agent/test_send_path_history_isolation.py` | | |
| `tests/agent/test_sequential_deadline_delegate_exempt.py` | | |
| `tests/agent/test_sequential_tool_interrupt.py` | | |
| `tests/agent/test_sequential_tool_timeout.py` | | |
| `tests/agent/test_skip_background_review.py` | | |
| `tests/agent/test_start_order_gate.py` | | |
| `tests/agent/test_steer.py` | | |
| `tests/agent/test_subagent_lifecycle.py` | | |
| `tests/agent/test_thinking_prefill_trailing_turn.py` | | |
| `tests/agent/test_title_input.py` | | |
| `tests/agent/test_tool_activity_heartbeat.py` | | |
| `tests/agent/test_tool_call_incremental_persistence.py` | | |
| `tests/agent/test_tool_executor_checkpoint_paths.py` | | |
| `tests/agent/test_truncated_tool_call_boost.py` | | |
| `tests/agent/test_turn_api_call_interrupt.py` | | |
| `tests/agent/test_turn_api_error_stream_parse.py` | | |
| `tests/agent/test_turn_author.py` | | |
| `tests/agent/test_turn_context.py` | | |
| `tests/agent/test_turn_context_compaction.py` | | |
| `tests/agent/test_turn_end_stall_diagnostics.py` | | |
| `tests/agent/test_turn_facade_lease.py` | | |
| `tests/agent/test_turn_finalizer_cleanup_guard.py` | | |
| `tests/agent/test_turn_finalizer_interrupt_alternation.py` | | |
| `tests/agent/test_turn_finalizer_iteration_limit_exit.py` | | |
| `tests/agent/test_turn_iteration_prep.py` | | |
| `tests/agent/test_turn_liveness.py` | | |
| `tests/agent/test_turn_liveness_watchdog.py` | | |
| `tests/agent/test_turn_recovery_autorecover.py` | | |
| `tests/agent/test_turn_retry_state.py` | | |
| `tests/agent/test_turn_summary.py` | | |
| `tests/agent/test_turn_usage_log_line.py` | | |
| `tests/agent/test_uncompressed_context_guardrail.py` | | |
| `tests/agent/test_unicode_ascii_codec.py` | | |
| `tests/agent/test_warning_presentation.py` | | |
| `tests/agent/test_welcome_error_identity.py` | | |
| `tests/agent/test_welcome_tier_recovery.py` | | |
| `tests/agent/transports/test_provider_wire_snapshot.py` | | |
| `tests/cron/test_cron_empty_turn_stays_silent.py` | | |
| `tests/gateway/test_api_server_transform_llm_output.py` | | |
| `tests/gateway/test_empty_sentinel_rewrite_copy.py` | | |
| `tests/gateway/test_warning_notification_contracts.py` | | |
| `tests/hermes_cli/test_peer_cmd.py` | | |
| `tests/hermes_cli/test_quiet_turn_author.py` | | |
| `tests/tools/test_approval_timeout_overflow.py` | | |
| `tests/tools/test_bot_relay.py` | | |
| `tests/tools/test_delegate_capacity_interrupt.py` | | |
| `tests/tools/test_preview_scope_gate.py` | | |

### compaction (105 files, floor 82.2%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_aux_stream_host_deadline.py` | dropped | auxiliary-model streaming deadlines: the summarizer is one ordinary provider call |
| `tests/agent/test_auxiliary_codex_completion_status.py` | dropped | Codex auxiliary client |
| `tests/agent/test_bedrock_adapter.py` | dropped | Bedrock provider |
| `tests/agent/test_budget_reasoning_details_exclusion.py` | dropped | accounting nuance for replayed reasoning; estimateMessage charges reasoning once |
| `tests/agent/test_cjk_token_estimation.py` | behaviour | internal/agent TestEstimateTokensMatchesPython (CJK, Cyrillic and ASCII samples from Python) |
| `tests/agent/test_codex_app_server_compaction.py` | dropped | Codex app-server compaction |
| `tests/agent/test_compaction_ops_notification_anchor.py` | dropped | notification and steer anchoring |
| `tests/agent/test_compaction_redaction_boundaries.py` | behaviour | internal/redact tests and TestSerializeForSummaryMatchesPython: everything entering a summary is redacted |
| `tests/agent/test_compress_context_progress_timeout.py` | dropped | progress-timeout plumbing for the Python status UI |
| `tests/agent/test_compress_focus.py` | behaviour | TestSummaryPromptMatchesPython covers the focus-topic prompt; the /compress command itself is task 4.1 |
| `tests/agent/test_compress_signal_leak.py` | dropped | Python signal/thread plumbing |
| `tests/agent/test_compressed_summary_metadata.py` | behaviour | TestCompactionOnLongRecordedSession: the summary row is flagged and found again on resume |
| `tests/agent/test_compression_attempt_lifecycle.py` | dropped | attempt ownership and staleness machinery for concurrent Python attempts |
| `tests/agent/test_compression_attempt_ownership.py` | dropped | attempt ownership machinery |
| `tests/agent/test_compression_attempt_telemetry.py` | dropped | outbound telemetry |
| `tests/agent/test_compression_auto_summary_fallback.py` | behaviour | TestRejectedSummaryFallsBackToUserWords, TestSummarizerFailureAbortsWithEscalatingCooldown |
| `tests/agent/test_compression_aux_fallback_route.py` | dropped | auxiliary summarizer routing and fallback chains |
| `tests/agent/test_compression_boundary.py` | behaviour | TestCompressPreservesStructureOnRandomTranscripts and FuzzCompress: tool groups never split, newest message kept |
| `tests/agent/test_compression_busy_steer_anchor.py` | dropped | steering is not ported |
| `tests/agent/test_compression_concurrent_fork.py` | dropped | forked concurrent compressions; one turn at a time per session (ErrTurnInProgress) |
| `tests/agent/test_compression_fallback_budget.py` | dropped | fallback-budget accounting for the auxiliary route |
| `tests/agent/test_compression_feasibility.py` | dropped | pre-LLM feasibility skip; the summarizer is always tried, a failure arms the cooldown |
| `tests/agent/test_compression_interrupt_protection.py` | behaviour | TestCancelledContextIsNotACooldown, TestInterruptDuringToolsClosesTranscript |
| `tests/agent/test_compression_last_assistant_anchor.py` | dropped | last-assistant tail anchor is not ported; the last user message anchors the tail |
| `tests/agent/test_compression_marker.py` | behaviour | TestElisionMatchesPython (elide and elide_middle, byte-identical) |
| `tests/agent/test_compression_persistence.py` | behaviour | internal/session TestReplaceLiveArchivesAndKeepsSessionID, TestCompactionOnLongRecordedSession |
| `tests/agent/test_compression_review.py` | dropped | Python review harness |
| `tests/agent/test_compression_rotation_state.py` | dropped | session rotation lineage: Go compaction keeps the session id |
| `tests/agent/test_compression_small_ctx_threshold_floor.py` | behaviour | TestThresholdsMatchPython (108 window, percent and reservation combinations) |
| `tests/agent/test_compression_stall_deterministic_fallback.py` | dropped | stall detection is not ported |
| `tests/agent/test_compression_stall_fallback.py` | dropped | stall detection is not ported |
| `tests/agent/test_compressor_actionable_tail_anchor.py` | behaviour | findTailCut keeps the latest user message in the tail; TestCompressReplacesTheMiddle, random transcripts |
| `tests/agent/test_compressor_assistant_tail_anchor.py` | dropped | assistant tail anchor is not ported |
| `tests/agent/test_compressor_fallback_update.py` | behaviour | TestRejectedSummaryFallsBackToUserWords (the fallback becomes the previous summary) |
| `tests/agent/test_compressor_historical_media.py` | dropped | images are not in Stage 1 |
| `tests/agent/test_compressor_image_tokens.py` | dropped | images are not in Stage 1 |
| `tests/agent/test_compressor_media_stripping.py` | dropped | images are not in Stage 1 |
| `tests/agent/test_compressor_stale_tool_images.py` | dropped | images are not in Stage 1 |
| `tests/agent/test_compressor_tail_cut_oob_fix.py` | behaviour | random-transcript and fuzz tests exercise every cut position |
| `tests/agent/test_compressor_tail_cut_tool_pair_floor.py` | behaviour | alignBackward; random-transcript property: no unanswered or orphaned tool calls |
| `tests/agent/test_compressor_tool_call_budget.py` | behaviour | estimateMessage prices the whole tool-call envelope (TestVerify via TestPendingToolRoundIsNeverPruned and tail tests) |
| `tests/agent/test_compressor_truncated_summary_guard.py` | dropped | the Provider interface does not expose a truncated summary; an empty summary aborts |
| `tests/agent/test_compressor_zero_user_guard.py` | behaviour | assemble forces a user-role summary when no user text survives (TestSummaryPlacementKeepsAlternation) |
| `tests/agent/test_context_compressor_cross_session_guard.py` | dropped | one Compressor per session: no cross-session state to guard |
| `tests/agent/test_context_compressor_custom_providers.py` | dropped | custom provider windows; Stage 1 is Anthropic only |
| `tests/agent/test_context_compressor_reasoning_fallback.py` | dropped | multi-provider reasoning fallbacks |
| `tests/agent/test_context_compressor_session_end_clears_state.py` | dropped | per-process compressor state; a new session builds a new Compressor |
| `tests/agent/test_context_compressor_structural_backoff.py` | dropped | structural no-op backoff; a no-op returns immediately and costs nothing |
| `tests/agent/test_context_compressor_summary_continuity.py` | behaviour | TestSecondCompressionUpdatesThePreviousSummary, TestCompactionOnLongRecordedSession |
| `tests/agent/test_context_compressor_summary_hook_compat.py` | dropped | plugin summary hooks |
| `tests/agent/test_context_compressor_task_heading.py` | behaviour | TestSummaryPromptMatchesPython (historical task heading in both prompt forms) |
| `tests/agent/test_context_compressor_temporal_anchoring.py` | behaviour | TestSummaryPromptMatchesPython (dated, past-tense rule) |
| `tests/agent/test_context_compressor_zero_user_provenance.py` | behaviour | TestSummaryPromptMatchesPython (the no-user-turn variant) |
| `tests/agent/test_context_engine.py` | dropped | pluggable context engines are not ported |
| `tests/agent/test_context_engine_message_uid.py` | dropped | pluggable context engines are not ported |
| `tests/agent/test_conversation_compression_manual.py` | later | manual /compress: re-bucket in task 4.1 |
| `tests/agent/test_cron_inflight_prompt_reappend.py` | later | cron single-prompt jobs: re-bucket in task 6.1 |
| `tests/agent/test_cursor_optimizations_parity.py` | dropped | Python performance parity checks |
| `tests/agent/test_estimator_parity.py` | behaviour | TestEstimateTokensMatchesPython |
| `tests/agent/test_ghost_skill_pruning.py` | dropped | skill-pruned markers are not ported |
| `tests/agent/test_grid_sweep_cli_tui_stream_diagnostics.py` | dropped | TUI stream diagnostics |
| `tests/agent/test_in_place_commit_rollback.py` | dropped | in-place persistence protocol of the Python store |
| `tests/agent/test_in_place_persist_marker.py` | dropped | in-place persistence protocol of the Python store |
| `tests/agent/test_in_place_preflight_rewind.py` | dropped | in-place persistence protocol of the Python store |
| `tests/agent/test_last_total_tokens.py` | dropped | Python usage-bookkeeping fields |
| `tests/agent/test_lean_single_aux_call.py` | behaviour | TestCompactionOnLongRecordedSession asserts exactly one summarizer call per compaction |
| `tests/agent/test_load_time_durability_stamp.py` | dropped | in-place persistence protocol of the Python store |
| `tests/agent/test_model_metadata.py` | dropped | model metadata discovery; the window is configuration |
| `tests/agent/test_native_compaction_summary_retention.py` | dropped | provider-native compaction is not used |
| `tests/agent/test_per_model_compression_threshold.py` | dropped | per-model threshold overrides; one model per session |
| `tests/agent/test_portal_tags.py` | dropped | portal attribution tags |
| `tests/agent/test_post_compression_trim.py` | dropped | allocator trimming is a Python/glibc concern |
| `tests/agent/test_pre_compress_checkpoint_contract.py` | later | memory checkpoint at compaction: re-bucket in task 5.x |
| `tests/agent/test_pre_compress_memory_context.py` | later | memory context handed to the summarizer: re-bucket in task 5.x |
| `tests/agent/test_preflight_timeout_typed_result.py` | dropped | Python preflight result typing |
| `tests/agent/test_proactive_prune_rearm_threshold.py` | dropped | proactive pruning is not ported; pruning happens inside compaction |
| `tests/agent/test_proactive_tool_result_pruning.py` | dropped | proactive pruning is not ported; pruning happens inside compaction |
| `tests/agent/test_resume_stale_active_task.py` | dropped | in-flight task replay is not ported |
| `tests/agent/test_salvage_grown_transcript.py` | dropped | transcript salvage after growth is not ported |
| `tests/agent/test_session_reset_fix.py` | dropped | per-process compressor state |
| `tests/agent/test_skill_todo_retention_parity.py` | dropped | skill and todo retention markers are not ported |
| `tests/agent/test_split_turn_compaction.py` | dropped | splitting an oversized active turn is not ported; an oversized turn is cut at a tool-group boundary and the handoff carries the request |
| `tests/agent/test_stale_replay_prune.py` | dropped | Codex replay items |
| `tests/agent/test_summarize_tool_result_type_safety.py` | behaviour | TestToolStubsMatchPython (malformed arguments and content); the type checks are compiler-covered |
| `tests/agent/test_summary_prefix_semantics.py` | behaviour | prompttext compaction.json holds the prefix verbatim; stripSummaryPrefix and TestSecondCompressionUpdatesThePreviousSummary |
| `tests/agent/test_summary_prefix_tool_use.py` | behaviour | the prefix's tools-remain-active clause is part of the verbatim text (compaction.json) |
| `tests/agent/test_summary_role_template_alternation.py` | behaviour | TestSummaryPlacementKeepsAlternation and the random-transcript alternation property |
| `tests/agent/test_switch_model_bedrock_sigv4.py` | dropped | model switching on Bedrock |
| `tests/agent/test_switch_model_reapplies_headers.py` | dropped | mid-session model switching changes the cached prefix; not supported |
| `tests/agent/test_switch_model_stale_base_url.py` | dropped | mid-session model switching changes the cached prefix; not supported |
| `tests/agent/test_token_estimator_shared.py` | behaviour | TestEstimateTokensMatchesPython |
| `tests/agent/test_tool_call_args_sanitizer.py` | behaviour | internal/agent TestRepairToolArgumentsMatchesPython (50 cases), FuzzRepairToolArguments |
| `tests/agent/test_turn_context_overflow_warning.py` | dropped | UI warning text |
| `tests/gateway/test_compaction_heartbeat_gateway_filter.py` | later | gateway progress messages: re-bucket in task 7.x |
| `tests/gateway/test_compression_progress_notices.py` | later | gateway progress messages: re-bucket in task 7.x |
| `tests/gateway/test_telegram_noise_filter.py` | dropped | gateway status filtering for the Python gateway |
| `tests/hermes_cli/test_cli_manual_compress.py` | later | manual /compress: re-bucket in task 4.1 |
| `tests/hermes_cli/test_context_switch_guard.py` | dropped | model switching guard; no mid-session model switch |
| `tests/hermes_cli/test_quiet_single_query.py` | dropped | Python CLI output mode |
| `tests/hermes_cli/test_shared_metrics_model_attribution.py` | dropped | outbound metrics |
| `tests/tools/test_computer_use_screenshot_dedup.py` | dropped | computer use is not in Stage 1 |
| `tests/tools/test_delegate_child_compression_cap.py` | later | child agents: re-bucket in task 3.5 |
| `tests/tui_gateway/test_busy_compression_demotion.py` | dropped | TUI gateway |
| `tests/tui_gateway/test_live_config_context_pin_mirror.py` | dropped | TUI gateway |
| `tests/tui_gateway/test_manual_compress_history_fence.py` | dropped | TUI gateway |

### approval (60 files, floor 90.2%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/acp_adapter/test_approval_isolation.py` | dropped | ACP adapter is not in Stage 1 |
| `tests/acp_adapter/test_permissions.py` | dropped | ACP adapter is not in Stage 1 |
| `tests/agent/test_auxiliary_explicit_cancellation.py` | dropped | auxiliary client |
| `tests/agent/test_runtime_self_protection.py` | later | refusing to delete the runtime's own interpreter: re-bucket in task 3.3 (terminal tool) |
| `tests/agent/test_shell_hooks.py` | dropped | shell hooks are not ported |
| `tests/cron/test_scheduler_cron_session_isolation.py` | later | cron session isolation: task 6.1 |
| `tests/gateway/test_api_server_runs.py` | dropped | API server platform is not in Stage 1 |
| `tests/gateway/test_approval_timeout_notice.py` | later | gateway approval surfaces: task 7.x |
| `tests/gateway/test_chat_completions_approval.py` | dropped | API server platform is not in Stage 1 |
| `tests/gateway/test_slack_approval_buttons.py` | dropped | Slack is not a Stage 1 platform |
| `tests/gateway/test_telegram_approval_buttons.py` | later | Telegram approval buttons: task 7.x (a Prompter implementation) |
| `tests/gateway/test_whatsapp_cloud.py` | dropped | WhatsApp is not a Stage 1 platform |
| `tests/hermes_cli/test_approval_transport.py` | dropped | plugin approval transports are not ported |
| `tests/hermes_cli/test_approvals_suggest.py` | dropped | CLI approvals suggestion command is not in Stage 1 |
| `tests/hermes_cli/test_approvals_test.py` | dropped | CLI approvals self-test command is not in Stage 1 |
| `tests/hermes_cli/test_cli_yolo_toggle.py` | later | the /yolo slash command: task 4.1 (Gate.SetSessionYolo) |
| `tests/hermes_cli/test_relay_shared_metrics_runtime.py` | dropped | Python web/relay/metrics plumbing |
| `tests/hermes_cli/test_web_server_idle_proof.py` | dropped | Python web/relay/metrics plumbing |
| `tests/tools/test_allowlist_legacy_config.py` | behaviour | TestLegacyKeysStillApprove; config.yaml reading is the caller's (task 4.1) |
| `tests/tools/test_allowlist_quoted_metachars.py` | behaviour | TestAllowlistShellOperatorMatchesPython (every corpus command) and TestAllowlistCommandTextAndGlobs |
| `tests/tools/test_approval.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions; gate tests in gate_test.go |
| `tests/tools/test_approval_bun_deno_eval.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_approval_cancelled_attribution.py` | behaviour | TestPromptFailuresFailClosed: an interrupt is cancelled, not a refusal by the user |
| `tests/tools/test_approval_deny_rules.py` | behaviour | TestUserDenyRulesBeatYoloAndContainers, TestFnmatchMatchesPython, TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_approval_gateway_wait_late_choice.py` | later | gateway round-trip wait: task 7.x |
| `tests/tools/test_approval_grep_in_substitution.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_approval_hook_session_id.py` | dropped | approval plugin hooks are not ported |
| `tests/tools/test_approval_interrupt.py` | behaviour | TestPromptFailuresFailClosed |
| `tests/tools/test_approval_plugin_hooks.py` | dropped | approval plugin hooks are not ported |
| `tests/tools/test_approval_unattended_presence.py` | behaviour | TestUnattendedPolicy |
| `tests/tools/test_approval_user_summary.py` | dropped | localized user summaries for the Python UI surfaces |
| `tests/tools/test_approved_command_clean_slate.py` | behaviour | TestPromptChoicesAndPersistence: ClearSession forgets approvals |
| `tests/tools/test_blocked_command_guidance.py` | behaviour | TestHardlineIsNeverBypassed, TestOversizedPayloadIsSavedForRecovery (the message texts are verbatim) |
| `tests/tools/test_browser_vault.py` | dropped | browser and computer use are not in Stage 1 |
| `tests/tools/test_cli_approval_exec_ask_leak.py` | dropped | Python environment-variable presence heuristics; the Go gate takes a Prompter |
| `tests/tools/test_code_kernel.py` | dropped | code kernels and Modal sandboxes are not in Stage 1 |
| `tests/tools/test_command_guards.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_computer_use_approval_isolation.py` | dropped | browser and computer use are not in Stage 1 |
| `tests/tools/test_computer_use_cua_0_10_permissions.py` | dropped | browser and computer use are not in Stage 1 |
| `tests/tools/test_computer_use_delivery_ladder.py` | dropped | browser and computer use are not in Stage 1 |
| `tests/tools/test_cron_not_interactive.py` | behaviour | TestUnattendedPolicy |
| `tests/tools/test_denial_circuit_breaker.py` | dropped | the denial circuit breaker belongs to smart approvals (guardian LLM), which are not ported |
| `tests/tools/test_docker_daemon_redirect.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_execute_code_approval_cluster.py` | later | execute_code approval: re-bucket with the code tool (not in Stage 1; dropped unless added) |
| `tests/tools/test_execution_flag_detection.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_gnu_long_option_abbreviation_bypass.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_hardline_blocklist.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions; TestHardlineIsNeverBypassed |
| `tests/tools/test_hardline_escaped_quote_state.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions |
| `tests/tools/test_human_input_hooks.py` | dropped | human-input plugin hooks are not ported |
| `tests/tools/test_mcp_elicitation.py` | dropped | MCP is not in Stage 1 |
| `tests/tools/test_mcp_trust_gating.py` | dropped | MCP is not in Stage 1 |
| `tests/tools/test_modal_sandbox_fixes.py` | dropped | code kernels and Modal sandboxes are not in Stage 1 |
| `tests/tools/test_multiplex_turn_parity.py` | dropped | multiplexed profiles are not ported |
| `tests/tools/test_request_tool_approval.py` | dropped | plugin-escalated tool approvals: plugins are not ported |
| `tests/tools/test_shell_bypass_denylist.py` | behaviour | TestDetectionMatchesPython: 1,486 command strings (every literal from the Python approval tests plus adversarial commands) get the same hardline and dangerous verdicts and descriptions; TestUserDenyRulesBeatYoloAndContainers |
| `tests/tools/test_smart_approval_injection.py` | dropped | smart approvals (guardian LLM) are not ported |
| `tests/tools/test_yolo_mode.py` | behaviour | TestYoloAndModeOffSkipThePromptButNotTheFloor |
| `tests/tui_gateway/test_approval_cancelled_on_reap.py` | dropped | TUI gateway process model is not ported |
| `tests/tui_gateway/test_cross_process_orphan_ownership.py` | dropped | TUI gateway process model is not ported |
| `tests/tui_gateway/test_session_finalize_reaps_kernels.py` | dropped | code kernels and Modal sandboxes are not in Stage 1 |

### tools-core (233 files, floor 76.6%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/lsp/test_backend_gate.py` | | |
| `tests/agent/lsp/test_custom_servers.py` | | |
| `tests/agent/lsp/test_diagnostics_field.py` | | |
| `tests/agent/lsp/test_shell_linter_lsp_skip.py` | | |
| `tests/agent/lsp/test_workspace_trust.py` | | |
| `tests/agent/test_agent_guardrails.py` | | |
| `tests/agent/test_codex_runtime_prompt_handoff.py` | | |
| `tests/agent/test_display_emoji.py` | | |
| `tests/agent/test_file_safety_credentials.py` | behaviour | TestSensitiveAndCredentialPathsAreRefused, TestReadGuards (guards.go) |
| `tests/agent/test_file_safety_session_state.py` | dropped | Python session-scoped file-safety state and in-place persistence |
| `tests/agent/test_inline_edit_persistence.py` | | |
| `tests/agent/test_interrupt_compat.py` | | |
| `tests/agent/test_relay_cwd.py` | | |
| `tests/agent/test_subagent_progress.py` | | |
| `tests/agent/test_subagent_stop_hook.py` | | |
| `tests/agent/test_terminal_approval_batch.py` | dropped | smart approvals, batched guards and sudo prompts are not ported |
| `tests/agent/test_token_persistence_non_cli.py` | | |
| `tests/agent/test_tool_guardrails.py` | | |
| `tests/agent/test_turn_user_intervened.py` | | |
| `tests/agent/test_verification_evidence.py` | | |
| `tests/agent/transports/test_codex_transport.py` | | |
| `tests/cron/test_cron_created_delivery.py` | | |
| `tests/cron/test_cron_run_stale_claim_reap.py` | | |
| `tests/cron/test_fire_forward_failure_stamp.py` | | |
| `tests/cron/test_ticker_stall.py` | | |
| `tests/gateway/test_api_server_toolset.py` | dropped | API server platform is not in Stage 1 |
| `tests/gateway/test_async_delivery_capability.py` | | |
| `tests/gateway/test_complete_path_at_filter.py` | | |
| `tests/gateway/test_cron_active_work_drain.py` | | |
| `tests/gateway/test_hygiene_deferred_work_drain.py` | | |
| `tests/gateway/test_review_command.py` | | |
| `tests/gateway/test_subagent_failure_notice.py` | | |
| `tests/hermes_cli/test_bang_shell_mode.py` | | |
| `tests/hermes_cli/test_cli_approval_ui.py` | | |
| `tests/hermes_cli/test_deferred_platform_client_tools.py` | | |
| `tests/hermes_cli/test_finite_chat_delegation.py` | | |
| `tests/hermes_cli/test_gateway_restart_loop.py` | | |
| `tests/hermes_cli/test_init_command.py` | | |
| `tests/hermes_cli/test_kanban_review_surfaces.py` | | |
| `tests/hermes_cli/test_plugin_dev.py` | | |
| `tests/hermes_cli/test_plugin_host.py` | | |
| `tests/hermes_cli/test_plugins_cmd_remove_bookkeeping.py` | | |
| `tests/hermes_cli/test_setup_blank_slate.py` | | |
| `tests/hermes_cli/test_shared_metrics_harness.py` | | |
| `tests/hermes_cli/test_shared_metrics_loop.py` | | |
| `tests/plugins/test_transform_tool_result_hook.py` | dropped | plugin hooks are not ported |
| `tests/plugins/web/test_web_search_provider_plugins.py` | | |
| `tests/test_hermes_home_key_cache.py` | | |
| `tests/test_model_tools_async_bridge.py` | dropped | async handler bridge: handlers are plain Go functions |
| `tests/test_sanitize_tool_error.py` | behaviour | TestErrorShapingMatchesPython (SanitizeError and Error against Python) |
| `tests/test_toolsets_plugin_platform_bundle.py` | dropped | plugin platform bundles are not ported |
| `tests/tools/test_accretion_caps.py` | | |
| `tests/tools/test_annotate_preview_tool.py` | | |
| `tests/tools/test_apply_layout_tool.py` | | |
| `tests/tools/test_approval_outcome_parity.py` | dropped | smart approvals, batched guards and sudo prompts are not ported |
| `tests/tools/test_async_delegation.py` | | |
| `tests/tools/test_binary_document_write_guard.py` | dropped | document-format write guard is not ported |
| `tests/tools/test_bot_mode_dm.py` | | |
| `tests/tools/test_browser_extension_router.py` | | |
| `tests/tools/test_browser_extension_router_wiring.py` | | |
| `tests/tools/test_browser_secret_exfil.py` | | |
| `tests/tools/test_budget_config.py` | dropped | per-model result budgets: one default limit (DefaultMaxResultChars) bounds a result |
| `tests/tools/test_clarify_tool.py` | | |
| `tests/tools/test_code_execution.py` | dropped | execute_code is not in Stage 1 |
| `tests/tools/test_code_execution_file_rpc.py` | dropped | execute_code is not in Stage 1 |
| `tests/tools/test_code_execution_modes.py` | dropped | execute_code is not in Stage 1 |
| `tests/tools/test_computer_use_native_platform.py` | | |
| `tests/tools/test_config_null_guard.py` | | |
| `tests/tools/test_connector_bridge_wiring.py` | | |
| `tests/tools/test_connector_dispatch_policy.py` | dropped | connector tools are not in Stage 1 |
| `tests/tools/test_connector_session_scope.py` | | |
| `tests/tools/test_connectors_mcp.py` | | |
| `tests/tools/test_connectors_tool.py` | | |
| `tests/tools/test_container_cwd_sanitize.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_cron_prompt_injection.py` | | |
| `tests/tools/test_cronjob_run_delivery_notice.py` | | |
| `tests/tools/test_cronjob_tools.py` | | |
| `tests/tools/test_deferral_fixes.py` | | |
| `tests/tools/test_delegate.py` | | |
| `tests/tools/test_delegate_apiserver_background.py` | | |
| `tests/tools/test_delegate_batch_tag.py` | | |
| `tests/tools/test_delegate_batch_validation.py` | | |
| `tests/tools/test_delegate_capability_inheritance.py` | | |
| `tests/tools/test_delegate_child_transcript_release.py` | | |
| `tests/tools/test_delegate_composite_toolsets.py` | later | delegation: task 3.5 |
| `tests/tools/test_delegate_cost_footer.py` | | |
| `tests/tools/test_delegate_depth_prompt.py` | | |
| `tests/tools/test_delegate_group_schema.py` | later | delegation: task 3.5 |
| `tests/tools/test_delegate_images.py` | | |
| `tests/tools/test_delegate_kanban_isolation.py` | | |
| `tests/tools/test_delegate_late_child_inherits_stop.py` | | |
| `tests/tools/test_delegate_liveness_timeout.py` | | |
| `tests/tools/test_delegate_native_vision.py` | | |
| `tests/tools/test_delegate_output_schema.py` | later | delegation: task 3.5 |
| `tests/tools/test_delegate_request_overrides.py` | | |
| `tests/tools/test_delegate_stale_heartbeat_collect.py` | | |
| `tests/tools/test_delegate_stale_wait_release.py` | | |
| `tests/tools/test_delegate_subagent_timeout_diagnostic.py` | | |
| `tests/tools/test_delegate_summary_budget.py` | later | delegation: task 3.5 |
| `tests/tools/test_delegate_sync_failure_echo_policy.py` | | |
| `tests/tools/test_delegate_timeout_cleanup.py` | | |
| `tests/tools/test_delegate_toolset_scope.py` | later | delegation: task 3.5 |
| `tests/tools/test_delegation_live_log.py` | | |
| `tests/tools/test_docker_environment.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_docker_mounted_host_session_cwd.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_docker_orphan_reaper_integration.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_docker_session_isolation.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_docker_teardown_drain.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_ensure_task_env.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_execute_code_lifecycle_guard_deadline.py` | dropped | execute_code is not in Stage 1 |
| `tests/tools/test_execute_helper_contract.py` | dropped | execute_code is not in Stage 1 |
| `tests/tools/test_file_operations.py` | behaviour | internal/tools/filetools: TestReadFileMatchesPython (25 reads against Python's read_file), TestPatchReplaceMatchesPython (18), TestV4AMatchesPython (31 patches) and tools_test.go |
| `tests/tools/test_file_operations_delete.py` | behaviour | V4A delete and move cases in TestV4AMatchesPython |
| `tests/tools/test_file_operations_edge_cases.py` | behaviour | internal/tools/filetools: TestReadFileMatchesPython (25 reads against Python's read_file), TestPatchReplaceMatchesPython (18), TestV4AMatchesPython (31 patches) and tools_test.go |
| `tests/tools/test_file_ops_cwd_tracking.py` | behaviour | resolve(): session cwd for relative paths, ~ expansion (every filetools test runs through it) |
| `tests/tools/test_file_ops_env_not_ready.py` | dropped | shell and container backends for file operations; Go reads the local filesystem |
| `tests/tools/test_file_ops_single_roundtrip.py` | dropped | shell and container backends for file operations; Go reads the local filesystem |
| `tests/tools/test_file_staleness.py` | behaviour | TestWriteRefusesToOverwriteWhatWasNeverRead, TestWriteRefusesAfterExternalChange, TestPartialReadIsNotAFullView, TestPatchWarnsWhenFileChangedSinceRead |
| `tests/tools/test_file_state_registry.py` | later | file-state tracking belongs with the file tools: task 3.3 |
| `tests/tools/test_file_tools.py` | behaviour | filetools/tools_test.go drives the real tools through a Toolbox |
| `tests/tools/test_file_tools_container_config.py` | dropped | shell and container backends for file operations; Go reads the local filesystem |
| `tests/tools/test_file_tools_cwd_resolution.py` | behaviour | resolve(): session cwd for relative paths, ~ expansion (every filetools test runs through it) |
| `tests/tools/test_file_tools_live.py` | behaviour | filetools/tools_test.go drives the real tools through a Toolbox |
| `tests/tools/test_file_tools_plugin_container_cwd.py` | dropped | shell and container backends for file operations; Go reads the local filesystem |
| `tests/tools/test_file_tools_tilde_profile.py` | behaviour | resolve(): session cwd for relative paths, ~ expansion (every filetools test runs through it) |
| `tests/tools/test_file_write_safety.py` | behaviour | TestSensitiveAndCredentialPathsAreRefused, TestReadGuards (guards.go) |
| `tests/tools/test_file_write_surrogate_roundtrip.py` | dropped | Python surrogateescape handling; Go strings are bytes and JSON input is valid UTF-8 |
| `tests/tools/test_finite_delegation_outcomes.py` | | |
| `tests/tools/test_gateway_cwd_contract.py` | later | gateway session working directories: task 7.x |
| `tests/tools/test_generation_source_confinement.py` | | |
| `tests/tools/test_image_generation_image_to_image.py` | | |
| `tests/tools/test_image_source.py` | | |
| `tests/tools/test_interrupted_command_cwd.py` | behaviour | TestWorkingDirectoryFollowsCd, TestInterruptStopsTheCommand |
| `tests/tools/test_kanban_unknown_arguments.py` | | |
| `tests/tools/test_known_file_write_baseline.py` | behaviour | TestWriteRefusesToOverwriteWhatWasNeverRead, TestWriteRefusesAfterExternalChange, TestPartialReadIsNotAFullView, TestPatchWarnsWhenFileChangedSinceRead |
| `tests/tools/test_line_ending_preservation.py` | behaviour | TestWriteKeepsBOMAndCRLF and the crlf/BOM patch goldens |
| `tests/tools/test_live_env_cwd_override_sanitize.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_macos_protected_search.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_managed_browserbase_and_modal.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_mcp_dynamic_discovery.py` | | |
| `tests/tools/test_mcp_initial_connect_shutdown.py` | | |
| `tests/tools/test_mcp_lazy_start.py` | | |
| `tests/tools/test_mcp_liveness_catalog.py` | | |
| `tests/tools/test_mcp_schema_cache.py` | dropped | MCP is not in Stage 1 |
| `tests/tools/test_mcp_shared_connection_resolved_identity.py` | | |
| `tests/tools/test_multiplex_tool_memo_scope.py` | | |
| `tests/tools/test_notebook_recovery_path.py` | | |
| `tests/tools/test_notify_on_complete.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_parse_env_var.py` | | |
| `tests/tools/test_patch_already_applied.py` | behaviour | fuzzy TestIsAlreadyApplied and the V4A already-applied golden case |
| `tests/tools/test_patch_parser.py` | behaviour | TestV4AMatchesPython (parse errors, markers, CRLF, implicit context lines, moves) |
| `tests/tools/test_patch_v4a_gate.py` | dropped | model-family gating of the V4A schema; the Go patch schema is replace-only and the handler accepts both |
| `tests/tools/test_process_heartbeat.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_project_tools.py` | | |
| `tests/tools/test_protected_instruction_cli_channel.py` | behaviour | TestProtectedInstructionFilesAlwaysAsk |
| `tests/tools/test_read_binary_type_disclosure.py` | behaviour | read golden cases png.raw, nul.raw and badutf8.txt |
| `tests/tools/test_read_extract.py` | dropped | document extraction, UTF-16 transcoding and Unicode-spelling retry are not ported |
| `tests/tools/test_read_file_utf8_binary_regression.py` | behaviour | read golden cases png.raw, nul.raw and badutf8.txt |
| `tests/tools/test_read_loop_detection.py` | behaviour | TestRepeatedIdenticalReadsAreCalledOut |
| `tests/tools/test_read_past_eof_note.py` | behaviour | read golden: three.txt offset 9 |
| `tests/tools/test_read_shell_line_clamp.py` | behaviour | read golden: long.txt (3,000-character line) and TestRunawayOutputIsCappedInMemory for the byte clamp |
| `tests/tools/test_read_special_file_guard.py` | behaviour | TestReadGuards (device paths, directories) |
| `tests/tools/test_read_unicode_filename_retry.py` | dropped | document extraction, UTF-16 transcoding and Unicode-spelling retry are not ported |
| `tests/tools/test_registry.py` | behaviour | internal/tools TestRegisterValidatesAndRejectsDuplicates, TestBuildSelectsAndGates, TestRunFailuresAreJSONErrors; the registry shape and type checks are compiler-covered |
| `tests/tools/test_remote_binary_write_guard.py` | dropped | document-format write guard is not ported |
| `tests/tools/test_resolve_path.py` | behaviour | resolve(): session cwd for relative paths, ~ expansion (every filetools test runs through it) |
| `tests/tools/test_safe_getcwd_permission_error.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_schema_sanitizer.py` | dropped | provider-illegal schema keys: Anthropic accepts the schemas the Go tools declare |
| `tests/tools/test_search_auto_multiline.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_budget_truncation.py` | later | search result budgets belong with the file tools: task 3.3 |
| `tests/tools/test_search_error_guard.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_files_engine_selection.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_giant_line_containment.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_hidden_dirs.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_native_rg.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_search_zero_match_and_multipath.py` | dropped | search_files is not a Stage 1 tool; searching is done with grep, rg and find through terminal |
| `tests/tools/test_session_cwd_store.py` | behaviour | TestWorkingDirectoryFollowsCd, TestInterruptStopsTheCommand |
| `tests/tools/test_shared_container_task_id.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_shell_arg_transport.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_ssh_environment.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_ssh_remote_cwd.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_ssh_session_cwd.py` | dropped | remote and container terminal backends are not in Stage 1 |
| `tests/tools/test_subagent_process_handoff.py` | | |
| `tests/tools/test_subagent_steer.py` | | |
| `tests/tools/test_subagent_sudo_prompt.py` | dropped | smart approvals, batched guards and sudo prompts are not ported |
| `tests/tools/test_terminal_backend_unavailable_reason.py` | dropped | plugin hooks, remote-backend deadlines and degradation are not ported |
| `tests/tools/test_terminal_cwd_echo.py` | behaviour | TestWorkingDirectoryFollowsCd, TestInterruptStopsTheCommand |
| `tests/tools/test_terminal_degraded_mode.py` | dropped | plugin hooks, remote-backend deadlines and degradation are not ported |
| `tests/tools/test_terminal_error_redaction.py` | behaviour | TestOutputIsCleanedAndRedacted |
| `tests/tools/test_terminal_foreground_timeout_cap.py` | behaviour | TestTimeoutIsClampedAndExplained (Python promotes to a background process; Go has none and clamps with a note) |
| `tests/tools/test_terminal_heredoc_background_guard.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_terminal_none_command_guard.py` | behaviour | TestEmptyCommandIsRefused |
| `tests/tools/test_terminal_output_transform_hook.py` | dropped | plugin hooks, remote-backend deadlines and degradation are not ported |
| `tests/tools/test_terminal_pre_guard_deadline.py` | dropped | plugin hooks, remote-backend deadlines and degradation are not ported |
| `tests/tools/test_terminal_requirements.py` | dropped | backend requirement checks; the terminal tool's Check is whether a shell exists |
| `tests/tools/test_terminal_task_cwd.py` | behaviour | TestWorkingDirectoryFollowsCd, TestInterruptStopsTheCommand |
| `tests/tools/test_terminal_tool.py` | behaviour | internal/tools/shell/shell_test.go |
| `tests/tools/test_terminal_tool_exception_redaction.py` | behaviour | TestOutputIsCleanedAndRedacted |
| `tests/tools/test_terminal_tool_pty_fallback.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_terminal_tool_requirements.py` | dropped | backend requirement checks; the terminal tool's Check is whether a shell exists |
| `tests/tools/test_terminal_truncation_spill.py` | behaviour | TestOutputIsBoundedHeadAndTail (the spill of the full output to a file is not ported) |
| `tests/tools/test_terminal_yield_to_background.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_timeout_transport_drain.py` | dropped | plugin hooks, remote-backend deadlines and degradation are not ported |
| `tests/tools/test_tool_result_storage.py` | dropped | oversized results are truncated head and tail (TestOversizedResultsKeepHeadAndTail); spilling to a file is not ported |
| `tests/tools/test_toolsets.py` | behaviour | TestBuildSelectsAndGates: toolset selection and exclusion; named composite toolsets are not ported |
| `tests/tools/test_tour_tool.py` | | |
| `tests/tools/test_tts_provider_authority.py` | | |
| `tests/tools/test_utf16_read.py` | dropped | document extraction, UTF-16 transcoding and Unicode-spelling retry are not ported |
| `tests/tools/test_video_analyze.py` | | |
| `tests/tools/test_video_generation_dynamic_schema.py` | dropped | video generation is not in Stage 1 |
| `tests/tools/test_watch_patterns.py` | dropped | background processes and PTYs are not in Stage 1 |
| `tests/tools/test_web_backend_capability_isolation.py` | dropped | provider capability registry is not ported |
| `tests/tools/test_web_extract_robustness.py` | behaviour | webtools: TestExtractPerURLFailuresDoNotSpoilTheRest, TestExtractCapsAtFiveURLsAndNeedsAny |
| `tests/tools/test_web_keyless_rescue.py` | dropped | keyless rescue ring is not ported (Tavily keyless mode is used directly) |
| `tests/tools/test_web_providers_brave_free.py` | dropped | Brave provider is not in Stage 1 |
| `tests/tools/test_web_providers_ddgs.py` | dropped | DDGS provider is not in Stage 1 |
| `tests/tools/test_web_providers_xai.py` | dropped | xAI provider is not in Stage 1 |
| `tests/tools/test_web_tools_config.py` | dropped | web backend selection config is not ported |
| `tests/tools/test_web_tools_dict_urls.py` | behaviour | TestExtractPerURLFailuresDoNotSpoilTheRest (non-string items become per-entry errors) |
| `tests/tools/test_web_tools_perplexity.py` | dropped | Perplexity provider is not in Stage 1 |
| `tests/tools/test_web_tools_tavily.py` | behaviour | webtools: TestSearchShapesAndAuth, TestSearchErrors |
| `tests/tools/test_web_tools_truncate.py` | behaviour | webtools: TestTruncateMatchesPython, TestBase64MatchesPython, TestClampMatchesPython, TestStoreFullTextWritesCapAndReplacesSymlink |
| `tests/tools/test_write_file_rewrite_hint.py` | dropped | the whole-file-rewrite cost hint is advisory text, not behaviour |
| `tests/tools/test_write_file_syntax_gate.py` | behaviour | TestWriteRefusesReadOutputAndBadJSON (JSON only; YAML and TOML gates are not ported) |
| `tests/tools/test_write_verification.py` | behaviour | TestWriteCreatesFilesAndParents (verified), patchReplace's post-write read-back |
| `tests/tools/test_xlsx_phonetic_text.py` | | |
| `tests/tools/test_zombie_process_cleanup.py` | behaviour | TestTimeoutKillsTheWholeProcessGroup, TestInterruptStopsTheCommand |
| `tests/tui_gateway/contracts/test_generated.py` | | |
| `tests/tui_gateway/test_backend_dir_entries_preconfirmed.py` | | |
| `tests/tui_gateway/test_init_targets_session_cwd.py` | | |
| `tests/tui_gateway/test_profile_terminal_scope_entrypoints.py` | | |
| `tests/tui_gateway/test_session_cwd_follow.py` | | |
| `tests/tui_gateway/test_session_cwd_profile_key.py` | | |

### memory (22 files, floor 87.4%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_api_content_sidecar.py` | | |
| `tests/agent/test_credits_cold_start.py` | | |
| `tests/agent/test_memory_async_sync.py` | | |
| `tests/agent/test_memory_boundary_commit.py` | | |
| `tests/agent/test_memory_context_dedupe.py` | | |
| `tests/agent/test_memory_provider.py` | | |
| `tests/agent/test_memory_recall_indicator.py` | | |
| `tests/agent/test_memory_session_switch.py` | | |
| `tests/agent/test_memory_skill_scaffolding.py` | | |
| `tests/agent/test_memory_sync_redaction.py` | | |
| `tests/agent/test_memory_user_id.py` | | |
| `tests/agent/test_streaming_context_scrubber.py` | | |
| `tests/gateway/test_memory_flush_on_reset.py` | | |
| `tests/plugins/memory/agent_memory/test_adapter.py` | | |
| `tests/plugins/memory/agent_memory/test_cli.py` | | |
| `tests/plugins/memory/agent_memory/test_discovery.py` | | |
| `tests/plugins/memory/agent_memory/test_importer.py` | | |
| `tests/plugins/memory/agent_memory/test_live.py` | | |
| `tests/plugins/memory/agent_memory/test_provider.py` | | |
| `tests/plugins/memory/agent_memory/test_skills_bridge.py` | | |
| `tests/plugins/memory/test_multiplex_memory_identity_scope.py` | | |
| `tests/plugins/memory/test_provider_threads_inherit_profile.py` | | |

### learning (3 files, floor 82.3%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/learning/test_agent_host.py` | | |
| `tests/agent/learning/test_learning_loop.py` | | |
| `tests/agent/learning/test_loop_wiring.py` | | |

### cron (186 files, floor 87.2%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_curator_backup.py` | | |
| `tests/agent/test_curator_reports.py` | | |
| `tests/agent/test_exit_cleanup_interrupt.py` | | |
| `tests/cron/test_agent_scheduling_gate.py` | | |
| `tests/cron/test_atomic_paused_creation.py` | | |
| `tests/cron/test_blueprint_catalog.py` | | |
| `tests/cron/test_bot_chat_cli_home.py` | | |
| `tests/cron/test_bot_chat_delivery_child_env.py` | | |
| `tests/cron/test_bot_chat_own_profile_multiplex_home.py` | | |
| `tests/cron/test_bot_chat_pending.py` | | |
| `tests/cron/test_bot_chat_pending_identity.py` | | |
| `tests/cron/test_bot_chat_timeout_marker.py` | | |
| `tests/cron/test_bot_chat_warning_replay.py` | | |
| `tests/cron/test_catchup_policy.py` | | |
| `tests/cron/test_claim_job_for_fire.py` | | |
| `tests/cron/test_cleanup_timeout.py` | | |
| `tests/cron/test_codex_execution_paths.py` | | |
| `tests/cron/test_compute_next_run_dst.py` | | |
| `tests/cron/test_compute_next_run_last_run_at.py` | | |
| `tests/cron/test_context_from_response_extraction.py` | | |
| `tests/cron/test_continuity_silent_audits.py` | | |
| `tests/cron/test_cron_bot_chat_delivery.py` | | |
| `tests/cron/test_cron_context_from.py` | | |
| `tests/cron/test_cron_delivery_redaction.py` | | |
| `tests/cron/test_cron_emfile_stall.py` | | |
| `tests/cron/test_cron_empty_payload.py` | | |
| `tests/cron/test_cron_execution_identity.py` | | |
| `tests/cron/test_cron_explicit_empty_toolsets.py` | | |
| `tests/cron/test_cron_failure_deliver.py` | | |
| `tests/cron/test_cron_failure_notice_copy.py` | | |
| `tests/cron/test_cron_failure_summarizer_inactivity.py` | | |
| `tests/cron/test_cron_home_target_profile_scope.py` | | |
| `tests/cron/test_cron_import_error_skew_hint.py` | | |
| `tests/cron/test_cron_inactivity_timeout.py` | | |
| `tests/cron/test_cron_incidents.py` | | |
| `tests/cron/test_cron_incidents_resolve.py` | | |
| `tests/cron/test_cron_kanban_env_isolation.py` | | |
| `tests/cron/test_cron_live_bot_delivery.py` | | |
| `tests/cron/test_cron_live_delivery_confirmation.py` | | |
| `tests/cron/test_cron_mcp_toolset_empty_block.py` | | |
| `tests/cron/test_cron_memory_contract.py` | | |
| `tests/cron/test_cron_multiplex_desktop_ticker_scope.py` | | |
| `tests/cron/test_cron_multiplex_inflight_release.py` | | |
| `tests/cron/test_cron_multiplex_liveness_and_pools.py` | | |
| `tests/cron/test_cron_multiplex_profile_route_preflight.py` | | |
| `tests/cron/test_cron_multiplex_served_profile_parity.py` | | |
| `tests/cron/test_cron_multiplex_shared_route_delivery.py` | | |
| `tests/cron/test_cron_multiplex_tick_ownership.py` | | |
| `tests/cron/test_cron_next_run_profile_timezone.py` | | |
| `tests/cron/test_cron_no_agent.py` | | |
| `tests/cron/test_cron_non_push_origin.py` | | |
| `tests/cron/test_cron_origin_synthetic_thread.py` | | |
| `tests/cron/test_cron_pinned_job_fallback.py` | | |
| `tests/cron/test_cron_pre_agent_fallback_notice.py` | | |
| `tests/cron/test_cron_profile_isolation.py` | | |
| `tests/cron/test_cron_prompt_injection_skill.py` | | |
| `tests/cron/test_cron_provider_pin.py` | | |
| `tests/cron/test_cron_reasoning_effort.py` | | |
| `tests/cron/test_cron_reconnect_only_rejection.py` | | |
| `tests/cron/test_cron_relay_delivery_guards.py` | | |
| `tests/cron/test_cron_relay_dm_origin_user_id.py` | | |
| `tests/cron/test_cron_relay_run_forward.py` | | |
| `tests/cron/test_cron_run_metrics.py` | | |
| `tests/cron/test_cron_script.py` | | |
| `tests/cron/test_cron_session_workdir.py` | | |
| `tests/cron/test_cron_thread_seed_dm_keying.py` | | |
| `tests/cron/test_cron_tick_stale_yield.py` | | |
| `tests/cron/test_cron_ticker_drift.py` | | |
| `tests/cron/test_cron_timezone_migration_catchup.py` | | |
| `tests/cron/test_cron_workdir.py` | | |
| `tests/cron/test_cronjob_gateway_liveness.py` | | |
| `tests/cron/test_dead_owner_claim_reclaim.py` | | |
| `tests/cron/test_delivery_queue.py` | | |
| `tests/cron/test_delivery_queue_retention_race.py` | | |
| `tests/cron/test_dispatch_lateness_stamp.py` | | |
| `tests/cron/test_due_scan_dst_fold.py` | | |
| `tests/cron/test_due_scan_save_failure.py` | | |
| `tests/cron/test_due_stale_cron_edit.py` | | |
| `tests/cron/test_duration_dst.py` | | |
| `tests/cron/test_estop.py` | | |
| `tests/cron/test_execution_ledger.py` | | |
| `tests/cron/test_execution_progress_stamp.py` | | |
| `tests/cron/test_external_worker_config_hooks.py` | | |
| `tests/cron/test_external_worker_secret_sources.py` | | |
| `tests/cron/test_fire_claim_lost_after_delivery.py` | | |
| `tests/cron/test_idle_tick_config_skip.py` | | |
| `tests/cron/test_inflight_stale_guard.py` | | |
| `tests/cron/test_jobs.py` | | |
| `tests/cron/test_jobs_changed_notify.py` | | |
| `tests/cron/test_jobs_crossprocess_lock.py` | | |
| `tests/cron/test_jobs_file_ownership.py` | | |
| `tests/cron/test_jobs_shrink_merge.py` | | |
| `tests/cron/test_last_failure_stamp.py` | | |
| `tests/cron/test_lifecycle_guard_budget.py` | | |
| `tests/cron/test_lifecycle_guard_heredoc_walk.py` | | |
| `tests/cron/test_lifecycle_guard_live_db.py` | | |
| `tests/cron/test_media_delivery_parity.py` | | |
| `tests/cron/test_media_send_timeout.py` | | |
| `tests/cron/test_mirror_origin_fallback.py` | | |
| `tests/cron/test_misfire_backstop_oneshot_grace.py` | | |
| `tests/cron/test_misfire_catchup.py` | | |
| `tests/cron/test_missed_window_catchup.py` | | |
| `tests/cron/test_monitor_kind.py` | | |
| `tests/cron/test_notepad.py` | | |
| `tests/cron/test_oneshot_dispatch_failure_run_claim.py` | | |
| `tests/cron/test_oneshot_grace_due_scan.py` | | |
| `tests/cron/test_oneshot_guard_warning.py` | | |
| `tests/cron/test_owner_start_time_drift.py` | | |
| `tests/cron/test_parallel_pool.py` | | |
| `tests/cron/test_persisted_error_rearm_legality.py` | | |
| `tests/cron/test_preflight_config.py` | | |
| `tests/cron/test_preflight_credential_verdict_names_home.py` | | |
| `tests/cron/test_preflight_managed_routes.py` | | |
| `tests/cron/test_quota_hold.py` | | |
| `tests/cron/test_recurring_persisted_error_recovery.py` | | |
| `tests/cron/test_recurring_wedge_selfheal.py` | | |
| `tests/cron/test_registration_ownership.py` | | |
| `tests/cron/test_relay_fronted_delivery.py` | | |
| `tests/cron/test_restart_safe_worker.py` | | |
| `tests/cron/test_restart_safe_worker_death_evidence.py` | | |
| `tests/cron/test_retirement_dispatch.py` | | |
| `tests/cron/test_rewrite_skill_refs.py` | | |
| `tests/cron/test_run_error_diagnostics.py` | | |
| `tests/cron/test_run_one_job.py` | | |
| `tests/cron/test_scheduled_occurrence.py` | | |
| `tests/cron/test_scheduler.py` | | |
| `tests/cron/test_scheduler_provider.py` | | |
| `tests/cron/test_scheduler_shutdown_guard.py` | | |
| `tests/cron/test_script_claim_heartbeat.py` | | |
| `tests/cron/test_sessiondb_init_hang.py` | | |
| `tests/cron/test_shutdown_interrupt.py` | | |
| `tests/cron/test_stale_running_recovery.py` | | |
| `tests/cron/test_suggestions.py` | | |
| `tests/cron/test_terminal_job_rearm.py` | | |
| `tests/cron/test_ticker_startup_survival.py` | | |
| `tests/cron/test_timezone.py` | | |
| `tests/cron/test_unreachable_retry.py` | | |
| `tests/cron/test_usage_audit_logger.py` | | |
| `tests/cron/test_warning_delivery_policy.py` | | |
| `tests/cron/test_warning_execution_outcome.py` | | |
| `tests/cron/test_warning_transport_contract.py` | | |
| `tests/e2e/core/delivery/test_cron_virtual_clock_soak.py` | | |
| `tests/gateway/relay/test_explicit_disable.py` | | |
| `tests/gateway/relay/test_relay_inchannel_continuable.py` | | |
| `tests/gateway/test_api_server_jobs.py` | | |
| `tests/gateway/test_cron_delivery_housekeeping.py` | | |
| `tests/gateway/test_cron_drain_floor.py` | | |
| `tests/gateway/test_cron_fire_startup_gate.py` | | |
| `tests/gateway/test_cron_fire_webhook.py` | | |
| `tests/gateway/test_cron_interrupt_notification.py` | | |
| `tests/gateway/test_cron_profile_gate.py` | | |
| `tests/gateway/test_drain_active_work_report.py` | | |
| `tests/gateway/test_restart_drain.py` | | |
| `tests/gateway/test_scale_to_zero_dashboard_client.py` | | |
| `tests/gateway/test_shutdown_kill_off_loop.py` | | |
| `tests/gateway/test_update_cron_drain.py` | | |
| `tests/hermes_cli/test_config_guard_surfaces.py` | | |
| `tests/hermes_cli/test_cron.py` | | |
| `tests/hermes_cli/test_cron_dashboard_off_loop.py` | | |
| `tests/hermes_cli/test_cron_delivery_targets_scope.py` | | |
| `tests/hermes_cli/test_cron_dispatch_visibility.py` | | |
| `tests/hermes_cli/test_cron_satellite_diagnostics.py` | | |
| `tests/hermes_cli/test_cron_status_next_run.py` | | |
| `tests/hermes_cli/test_cron_status_profile_isolation.py` | | |
| `tests/hermes_cli/test_data_uninstall_holders.py` | | |
| `tests/hermes_cli/test_desktop_cron_ticker_gateway_standdown.py` | | |
| `tests/hermes_cli/test_desktop_cron_ticker_profiles.py` | | |
| `tests/hermes_cli/test_multiplex_host_topology_reporting.py` | | |
| `tests/hermes_cli/test_sqlite_util_canonical.py` | | |
| `tests/hermes_cli/test_update_stale_gateway_yield.py` | | |
| `tests/hermes_cli/test_web_server_cron_profiles.py` | | |
| `tests/hermes_cli/test_web_server_idle_exit.py` | | |
| `tests/hermes_cli/test_webhook_cli.py` | | |
| `tests/hermes_cli/test_worktree_pushed_tier.py` | | |
| `tests/monitoring/test_cron_health_export.py` | | |
| `tests/plugins/test_chronos_cron.py` | | |
| `tests/plugins/test_plugin_automation_blueprints.py` | | |
| `tests/skills/test_product_price_monitor_skill.py` | | |
| `tests/skills/test_skill_blueprint_contracts.py` | | |
| `tests/skills/test_weekly_review_planning_skill.py` | | |
| `tests/test_atomic_json_writers_unified.py` | | |
| `tests/tools/test_blueprints.py` | | |
| `tests/tools/test_cronjob_run_background.py` | | |
| `tests/tools/test_cronjob_run_immediate.py` | | |
| `tests/tools/test_cronjob_run_primary_routed.py` | | |
| `tests/tools/test_send_message_plugin_extensibility.py` | | |

### gateway-core (556 files, floor 83.8%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/agent/test_generated_media_durable.py` | | |
| `tests/agent/test_multiplex_base_url_scope.py` | | |
| `tests/agent/test_openai_ultrafast.py` | | |
| `tests/agent/test_proxy_bypass_shared.py` | | |
| `tests/agent/test_refine_snapshot_isolation.py` | | |
| `tests/agent/test_secret_scope_tier1_migration.py` | | |
| `tests/agent/test_surrogate_chokepoints.py` | | |
| `tests/e2e/test_platform_commands.py` | | |
| `tests/gateway/relay/test_identity_token_resolver.py` | | |
| `tests/gateway/relay/test_provision_secret_optional.py` | | |
| `tests/gateway/relay/test_relay_adapter.py` | | |
| `tests/gateway/relay/test_relay_egress_declines.py` | | |
| `tests/gateway/relay/test_relay_inbound_dedupe.py` | | |
| `tests/gateway/relay/test_relay_interactive.py` | | |
| `tests/gateway/relay/test_relay_media.py` | | |
| `tests/gateway/relay/test_relay_multiplatform.py` | | |
| `tests/gateway/relay/test_relay_per_platform_caps.py` | | |
| `tests/gateway/relay/test_relay_policy_send.py` | | |
| `tests/gateway/relay/test_relay_roundtrip.py` | | |
| `tests/gateway/relay/test_relay_roundtrip_telegram.py` | | |
| `tests/gateway/relay/test_relay_slack_dm_streaming.py` | | |
| `tests/gateway/relay/test_relay_slack_prompt_dm_root.py` | | |
| `tests/gateway/relay/test_relay_slack_unfurl.py` | | |
| `tests/gateway/relay/test_relay_threads.py` | | |
| `tests/gateway/relay/test_self_provision.py` | | |
| `tests/gateway/relay/test_wire_user_identity.py` | | |
| `tests/gateway/relay/test_wire_voice_media.py` | | |
| `tests/gateway/test_abandoned_turn_process_cleanup.py` | | |
| `tests/gateway/test_active_session_text_merge.py` | | |
| `tests/gateway/test_active_turn_recovery.py` | | |
| `tests/gateway/test_adapter_connect_classification.py` | | |
| `tests/gateway/test_adapter_settings_scoped_precedence.py` | | |
| `tests/gateway/test_agent_cache_release_profile_scope.py` | | |
| `tests/gateway/test_agent_error_reply_401_hint.py` | | |
| `tests/gateway/test_agent_loop_stopped_hook.py` | | |
| `tests/gateway/test_agents_command_delegations.py` | | |
| `tests/gateway/test_allowlist_qualified_ids.py` | | |
| `tests/gateway/test_allowlist_warning_platform_gate.py` | | |
| `tests/gateway/test_api_server_bind_guard.py` | | |
| `tests/gateway/test_api_server_reasoning_stream.py` | | |
| `tests/gateway/test_approval_prompt_redaction.py` | | |
| `tests/gateway/test_approval_send_timeout_ambiguity.py` | | |
| `tests/gateway/test_approvals_command.py` | | |
| `tests/gateway/test_approve_deny_commands.py` | | |
| `tests/gateway/test_async_delegation_session_binding.py` | | |
| `tests/gateway/test_async_media_cache.py` | | |
| `tests/gateway/test_audio_cache.py` | | |
| `tests/gateway/test_auth_fallback.py` | | |
| `tests/gateway/test_auto_continue.py` | | |
| `tests/gateway/test_auto_reset_evicts_cached_agent.py` | | |
| `tests/gateway/test_auto_skill_title_input.py` | | |
| `tests/gateway/test_auto_voice_reply_format.py` | | |
| `tests/gateway/test_background_command.py` | | |
| `tests/gateway/test_background_process_notifications.py` | | |
| `tests/gateway/test_background_process_served_apiserver_wake.py` | | |
| `tests/gateway/test_base_auto_tts_output_format.py` | | |
| `tests/gateway/test_base_topic_sessions.py` | | |
| `tests/gateway/test_base_warning_owner_scope.py` | | |
| `tests/gateway/test_baseexception_turn_notify.py` | | |
| `tests/gateway/test_bot_loop_guard.py` | | |
| `tests/gateway/test_bounded_adapter_teardown.py` | | |
| `tests/gateway/test_branch_routing_columns.py` | | |
| `tests/gateway/test_branch_thread_command.py` | | |
| `tests/gateway/test_builtin_adapter_factory.py` | | |
| `tests/gateway/test_busy_command.py` | | |
| `tests/gateway/test_busy_followup_after_session_release.py` | | |
| `tests/gateway/test_busy_followup_guard_release.py` | | |
| `tests/gateway/test_busy_redirect_anchor.py` | | |
| `tests/gateway/test_busy_requeue_hot_loop.py` | | |
| `tests/gateway/test_busy_session_ack.py` | | |
| `tests/gateway/test_busy_session_auth_bypass.py` | | |
| `tests/gateway/test_busy_session_profile_scope.py` | | |
| `tests/gateway/test_busy_steer_origin.py` | | |
| `tests/gateway/test_busy_steer_subagent_fanout.py` | | |
| `tests/gateway/test_busy_wake_admission.py` | | |
| `tests/gateway/test_cached_agent_history_guard.py` | | |
| `tests/gateway/test_cancel_background_drain.py` | | |
| `tests/gateway/test_channel_directory_connected_only.py` | | |
| `tests/gateway/test_channel_overrides.py` | | |
| `tests/gateway/test_checkpoint_config.py` | | |
| `tests/gateway/test_checkpoint_prune_housekeeping.py` | | |
| `tests/gateway/test_choice_picker.py` | | |
| `tests/gateway/test_cjk_fts_config_bridge.py` | | |
| `tests/gateway/test_clarify_active_session_bypass.py` | | |
| `tests/gateway/test_clarify_card_retire_on_timeout.py` | | |
| `tests/gateway/test_clarify_delivery_fallback.py` | | |
| `tests/gateway/test_clarify_progress_leak.py` | | |
| `tests/gateway/test_clarify_thread_followup_not_swallowed.py` | | |
| `tests/gateway/test_clean_shutdown_marker.py` | | |
| `tests/gateway/test_cleanup_off_loop.py` | | |
| `tests/gateway/test_code_fence_tracking.py` | | |
| `tests/gateway/test_command_bypass_active_session.py` | | |
| `tests/gateway/test_completion_admission.py` | | |
| `tests/gateway/test_completion_session_boundary.py` | | |
| `tests/gateway/test_compress_command.py` | | |
| `tests/gateway/test_compress_focus.py` | | |
| `tests/gateway/test_compress_plugin_engine.py` | | |
| `tests/gateway/test_compress_preview.py` | | |
| `tests/gateway/test_compression_failure_session_sync.py` | | |
| `tests/gateway/test_compression_in_flight_check.py` | | |
| `tests/gateway/test_compression_interrupt_demotion.py` | | |
| `tests/gateway/test_config.py` | | |
| `tests/gateway/test_config_driven_access_policy.py` | | |
| `tests/gateway/test_context_ref_expansion_runtime.py` | | |
| `tests/gateway/test_conversation_scope_funnel.py` | | |
| `tests/gateway/test_cron_shutdown_drain.py` | | |
| `tests/gateway/test_custom_provider_request_overrides.py` | | |
| `tests/gateway/test_dead_targets.py` | | |
| `tests/gateway/test_decline_fallback_suppression.py` | | |
| `tests/gateway/test_delivery.py` | | |
| `tests/gateway/test_delivery_ledger.py` | | |
| `tests/gateway/test_delivery_ledger_process_home.py` | | |
| `tests/gateway/test_delivery_ledger_producer.py` | | |
| `tests/gateway/test_destructive_slash_always_persist_report.py` | | |
| `tests/gateway/test_destructive_slash_confirm.py` | | |
| `tests/gateway/test_diagnostic_wake_presentation.py` | | |
| `tests/gateway/test_diff_command.py` | | |
| `tests/gateway/test_discord_auto_thread_prompt_parity.py` | | |
| `tests/gateway/test_discord_auto_thread_rename_pin.py` | | |
| `tests/gateway/test_discord_bot_auth_bypass.py` | | |
| `tests/gateway/test_discord_channel_prompts.py` | | |
| `tests/gateway/test_discord_clarify_buttons.py` | | |
| `tests/gateway/test_discord_document_handling.py` | | |
| `tests/gateway/test_discord_model_picker.py` | | |
| `tests/gateway/test_discord_model_picker_partition.py` | | |
| `tests/gateway/test_discord_pending_text_batch_shutdown.py` | | |
| `tests/gateway/test_discord_platform_events.py` | | |
| `tests/gateway/test_discord_reactions.py` | | |
| `tests/gateway/test_discord_slash_auth.py` | | |
| `tests/gateway/test_discord_slash_commands.py` | | |
| `tests/gateway/test_discord_triggering_note_persistence.py` | | |
| `tests/gateway/test_discord_turn_prompt_parity.py` | | |
| `tests/gateway/test_discord_username_allowlist_env_reload.py` | | |
| `tests/gateway/test_discord_view_base_parity.py` | | |
| `tests/gateway/test_discord_voice_role_grant.py` | | |
| `tests/gateway/test_discord_voice_stale_binding.py` | | |
| `tests/gateway/test_document_cache.py` | | |
| `tests/gateway/test_document_context_note.py` | | |
| `tests/gateway/test_draft_id_restart_uniqueness.py` | | |
| `tests/gateway/test_duplicate_reply_suppression.py` | | |
| `tests/gateway/test_duplicate_user_message.py` | | |
| `tests/gateway/test_email.py` | | |
| `tests/gateway/test_empty_model_fallback.py` | | |
| `tests/gateway/test_empty_model_recovery.py` | | |
| `tests/gateway/test_ephemeral_reply.py` | | |
| `tests/gateway/test_escape_reasoning_fences.py` | | |
| `tests/gateway/test_exec_approval_template.py` | | |
| `tests/gateway/test_exec_approval_timeout_notice.py` | | |
| `tests/gateway/test_executor_pool_isolation.py` | | |
| `tests/gateway/test_external_drain_control.py` | | |
| `tests/gateway/test_extract_local_files.py` | | |
| `tests/gateway/test_failure_writer_ownership.py` | | |
| `tests/gateway/test_fallback_chain_reload.py` | | |
| `tests/gateway/test_fast_command.py` | | |
| `tests/gateway/test_fifo_overflow_rescue.py` | | |
| `tests/gateway/test_finalize_session_off_loop.py` | | |
| `tests/gateway/test_first_turn_session_meta_rebaseline.py` | | |
| `tests/gateway/test_footer_command_mid_run.py` | | |
| `tests/gateway/test_free_tier_gateway_boot.py` | | |
| `tests/gateway/test_free_tier_startup_notice.py` | | |
| `tests/gateway/test_fresh_reset_skill_injection.py` | | |
| `tests/gateway/test_gateway_command_dispatch_minimal.py` | | |
| `tests/gateway/test_gateway_command_help.py` | | |
| `tests/gateway/test_gateway_history_persistence_stamp.py` | | |
| `tests/gateway/test_gateway_mcp_oauth_unattended.py` | | |
| `tests/gateway/test_gateway_process_exit.py` | | |
| `tests/gateway/test_gateway_shutdown.py` | | |
| `tests/gateway/test_gateway_silence_tokens.py` | | |
| `tests/gateway/test_gateway_trust_env.py` | | |
| `tests/gateway/test_goal_continuation_drain.py` | | |
| `tests/gateway/test_goal_gate_access.py` | | |
| `tests/gateway/test_goal_max_turns_config.py` | | |
| `tests/gateway/test_goal_resume_restart.py` | | |
| `tests/gateway/test_goal_status_notice.py` | | |
| `tests/gateway/test_goal_verdict_send.py` | | |
| `tests/gateway/test_grid_sweep_gateway_diagnostics.py` | | |
| `tests/gateway/test_handoff_secondary_profile_adapter.py` | | |
| `tests/gateway/test_handoff_thread_session_key.py` | | |
| `tests/gateway/test_handoff_watcher_multiprofile.py` | | |
| `tests/gateway/test_heartbeat_acceptance.py` | | |
| `tests/gateway/test_heartbeat_execution_ownership.py` | | |
| `tests/gateway/test_heartbeat_poller.py` | | |
| `tests/gateway/test_heartbeat_session_boundaries.py` | | |
| `tests/gateway/test_heartbeat_watch_lifecycle.py` | | |
| `tests/gateway/test_heartbeat_watch_restore.py` | | |
| `tests/gateway/test_history_media_current_turn.py` | | |
| `tests/gateway/test_home_target_env_var.py` | | |
| `tests/gateway/test_hosted_room_execution_policy.py` | | |
| `tests/gateway/test_hosted_room_gateway_lifecycle.py` | | |
| `tests/gateway/test_identity_canonical_ingress.py` | | |
| `tests/gateway/test_image_input_routing_runtime.py` | | |
| `tests/gateway/test_import_preserves_tui_cwd.py` | | |
| `tests/gateway/test_inbound_media_profile_scope.py` | | |
| `tests/gateway/test_incomplete_gateway_turns.py` | | |
| `tests/gateway/test_interactive_prompt_base.py` | | |
| `tests/gateway/test_interim_send_lanes.py` | | |
| `tests/gateway/test_internal_event_bypass_pairing.py` | | |
| `tests/gateway/test_internal_event_never_interrupts_busy_session.py` | | |
| `tests/gateway/test_internal_event_pin_wiring.py` | | |
| `tests/gateway/test_internal_notification_marker.py` | | |
| `tests/gateway/test_interrupt_keeps_parked_internal_wake.py` | | |
| `tests/gateway/test_kanban_changes_requested_notifier.py` | | |
| `tests/gateway/test_kanban_multiplex_served_profile_parity.py` | | |
| `tests/gateway/test_kanban_notifier.py` | | |
| `tests/gateway/test_kanban_notifier_apiserver_wake.py` | | |
| `tests/gateway/test_kanban_notifier_served_apiserver_wake.py` | | |
| `tests/gateway/test_kanban_notifier_wake_only_ordering.py` | | |
| `tests/gateway/test_kanban_notifier_zero_sub_gate.py` | | |
| `tests/gateway/test_kanban_route_anchor_capture.py` | | |
| `tests/gateway/test_kanban_routed_transport.py` | | |
| `tests/gateway/test_kanban_wake_acceptance.py` | | |
| `tests/gateway/test_kanban_wake_scope.py` | | |
| `tests/gateway/test_keep_typing_timeout.py` | | |
| `tests/gateway/test_launchd_exit_timeout_drain_cap.py` | | |
| `tests/gateway/test_local_model_connection_reply.py` | | |
| `tests/gateway/test_login_command.py` | | |
| `tests/gateway/test_loop_command.py` | | |
| `tests/gateway/test_loop_exception_handler.py` | | |
| `tests/gateway/test_max_concurrent_sessions.py` | | |
| `tests/gateway/test_mcp_reload_refreshes_cached_agents.py` | | |
| `tests/gateway/test_mcp_shutdown_profile_passes.py` | | |
| `tests/gateway/test_media_cache.py` | | |
| `tests/gateway/test_media_cache_cleanup_signature.py` | | |
| `tests/gateway/test_media_download_retry.py` | | |
| `tests/gateway/test_media_extraction.py` | | |
| `tests/gateway/test_media_only_outcome.py` | | |
| `tests/gateway/test_media_resend_dedup.py` | | |
| `tests/gateway/test_media_spaced_paths_and_history_dedupe.py` | | |
| `tests/gateway/test_media_tag_cleanup.py` | | |
| `tests/gateway/test_media_tag_formatting_variants.py` | | |
| `tests/gateway/test_media_tag_separator.py` | | |
| `tests/gateway/test_media_terminal_sentinel.py` | | |
| `tests/gateway/test_memory_trim_housekeeping.py` | | |
| `tests/gateway/test_message_timestamps.py` | | |
| `tests/gateway/test_mixed_attachment_routing.py` | | |
| `tests/gateway/test_moa_one_shot_restore.py` | | |
| `tests/gateway/test_model_command_async_offload.py` | | |
| `tests/gateway/test_model_command_context_offload.py` | | |
| `tests/gateway/test_model_command_custom_providers.py` | | |
| `tests/gateway/test_model_command_expensive_confirm.py` | | |
| `tests/gateway/test_model_command_flat_string_config.py` | | |
| `tests/gateway/test_model_command_profile_config.py` | | |
| `tests/gateway/test_model_command_reasoning_flag.py` | | |
| `tests/gateway/test_model_command_request_overrides.py` | | |
| `tests/gateway/test_model_picker_persist.py` | | |
| `tests/gateway/test_model_switch_persistence.py` | | |
| `tests/gateway/test_multiplex_adapter_session_key_namespace.py` | | |
| `tests/gateway/test_multiplex_background_task_scope.py` | | |
| `tests/gateway/test_multiplex_busy_input_mode.py` | | |
| `tests/gateway/test_multiplex_docker_media_delivery_scope.py` | | |
| `tests/gateway/test_multiplex_interactive_auth.py` | | |
| `tests/gateway/test_multiplex_lifecycle.py` | | |
| `tests/gateway/test_multiplex_log_routing.py` | | |
| `tests/gateway/test_multiplex_mcp_discovery.py` | | |
| `tests/gateway/test_multiplex_notice_egress_profile_adapter.py` | | |
| `tests/gateway/test_multiplex_phase0.py` | | |
| `tests/gateway/test_multiplex_primary_token_scope.py` | | |
| `tests/gateway/test_multiplex_process_global_sinks.py` | | |
| `tests/gateway/test_multiplex_profile_authz.py` | | |
| `tests/gateway/test_multiplex_routing_authz.py` | | |
| `tests/gateway/test_multiplex_secondary_port_binding_env.py` | | |
| `tests/gateway/test_multiplex_served_profile_parity.py` | | |
| `tests/gateway/test_multiplex_session_db_profile_scope.py` | | |
| `tests/gateway/test_multiplex_transport_matrix.py` | | |
| `tests/gateway/test_native_image_buffer_isolation.py` | | |
| `tests/gateway/test_native_producer_owner_matrix.py` | | |
| `tests/gateway/test_native_stream_warning_composition.py` | | |
| `tests/gateway/test_normalize_empty_agent_response.py` | | |
| `tests/gateway/test_notice_delivery.py` | | |
| `tests/gateway/test_notice_rendering.py` | | |
| `tests/gateway/test_notify_fatal_error_shield.py` | | |
| `tests/gateway/test_ogg_opus_transcode_routing.py` | | |
| `tests/gateway/test_own_policy_startup_gate.py` | | |
| `tests/gateway/test_pairing.py` | | |
| `tests/gateway/test_pairing_allowlist_bypass.py` | | |
| `tests/gateway/test_pending_drain_no_recursion.py` | | |
| `tests/gateway/test_pending_drain_race.py` | | |
| `tests/gateway/test_pending_queue_spool.py` | | |
| `tests/gateway/test_pending_steer_drain.py` | | |
| `tests/gateway/test_personality_routed_profile.py` | | |
| `tests/gateway/test_pii_redaction.py` | | |
| `tests/gateway/test_plaintext_approval_routing.py` | | |
| `tests/gateway/test_planned_restart_notice_multiplex.py` | | |
| `tests/gateway/test_planned_stop_watcher.py` | | |
| `tests/gateway/test_platform_adapter_i18n.py` | | |
| `tests/gateway/test_platform_authz_scope.py` | | |
| `tests/gateway/test_platform_base.py` | | |
| `tests/gateway/test_platform_entry_plugin_seams.py` | | |
| `tests/gateway/test_platform_reconnect_fd_leak.py` | | |
| `tests/gateway/test_platform_shared_metrics.py` | | |
| `tests/gateway/test_plugin_check_housekeeping.py` | | |
| `tests/gateway/test_plugin_message_injection.py` | | |
| `tests/gateway/test_plugin_origin_injection.py` | | |
| `tests/gateway/test_pm_activation.py` | | |
| `tests/gateway/test_post_delivery_callback_chaining.py` | | |
| `tests/gateway/test_post_gateway_admission.py` | | |
| `tests/gateway/test_post_stream_media_delivery.py` | | |
| `tests/gateway/test_pre_agent_fallback_notice.py` | | |
| `tests/gateway/test_pre_gateway_dispatch.py` | | |
| `tests/gateway/test_priority_path_compression_demotion.py` | | |
| `tests/gateway/test_profile_isolation_runtime.py` | | |
| `tests/gateway/test_profile_resolution.py` | | |
| `tests/gateway/test_profile_spool_recovery.py` | | |
| `tests/gateway/test_prompt_decline_no_fallback.py` | | |
| `tests/gateway/test_prompt_tail_freeze.py` | | |
| `tests/gateway/test_proxy_mode.py` | | |
| `tests/gateway/test_purge_profile_routing.py` | | |
| `tests/gateway/test_queue_command.py` | | |
| `tests/gateway/test_queue_consumption.py` | | |
| `tests/gateway/test_queued_final_ledger.py` | | |
| `tests/gateway/test_queued_followup_final_delivery.py` | | |
| `tests/gateway/test_queued_followup_processing_hooks.py` | | |
| `tests/gateway/test_queued_native_image_session_key.py` | | |
| `tests/gateway/test_reaped_eviction_interrupts_run.py` | | |
| `tests/gateway/test_reaped_session_recovery.py` | | |
| `tests/gateway/test_reasoning_command.py` | | |
| `tests/gateway/test_reconnect_attention_profile_scope.py` | | |
| `tests/gateway/test_rekey_profile_routing.py` | | |
| `tests/gateway/test_relay_completion_injection_routing.py` | | |
| `tests/gateway/test_relay_delivery_followups.py` | | |
| `tests/gateway/test_relay_final_delivery_incident.py` | | |
| `tests/gateway/test_relay_injection_egress_priming.py` | | |
| `tests/gateway/test_relay_seal_failure.py` | | |
| `tests/gateway/test_relay_source_platform_metrics.py` | | |
| `tests/gateway/test_relay_upstream_authz.py` | | |
| `tests/gateway/test_reload_signal_logs_graceful_restart.py` | | |
| `tests/gateway/test_reload_skills_command.py` | | |
| `tests/gateway/test_reload_skills_discord_resync.py` | | |
| `tests/gateway/test_remote_media_fetch.py` | | |
| `tests/gateway/test_replace_child_reap.py` | | |
| `tests/gateway/test_replace_ownership_guard.py` | | |
| `tests/gateway/test_replay_entry_fields.py` | | |
| `tests/gateway/test_reply_to_injection.py` | | |
| `tests/gateway/test_reset_button_deadlock.py` | | |
| `tests/gateway/test_restart_after_turn.py` | | |
| `tests/gateway/test_restart_notice_replay.py` | | |
| `tests/gateway/test_restart_notification.py` | | |
| `tests/gateway/test_restart_redelivery_dedup.py` | | |
| `tests/gateway/test_restart_resume_pending.py` | | |
| `tests/gateway/test_restart_service_detection.py` | | |
| `tests/gateway/test_resume_command.py` | | |
| `tests/gateway/test_retry_replacement.py` | | |
| `tests/gateway/test_run_cleanup_progress.py` | | |
| `tests/gateway/test_run_progress_interrupt.py` | | |
| `tests/gateway/test_run_progress_topics.py` | | |
| `tests/gateway/test_runner_fatal_adapter.py` | | |
| `tests/gateway/test_runner_startup_failures.py` | | |
| `tests/gateway/test_running_agent_session_toggles.py` | | |
| `tests/gateway/test_runtime_config_env_expansion.py` | | |
| `tests/gateway/test_runtime_env_reload_config_authority.py` | | |
| `tests/gateway/test_runtime_provider_override_target_model.py` | | |
| `tests/gateway/test_safe_adapter_disconnect.py` | | |
| `tests/gateway/test_save_command_delivery.py` | | |
| `tests/gateway/test_scale_to_zero_watcher.py` | | |
| `tests/gateway/test_send_error_classification.py` | | |
| `tests/gateway/test_send_image_file.py` | | |
| `tests/gateway/test_send_multiple_images.py` | | |
| `tests/gateway/test_send_retry.py` | | |
| `tests/gateway/test_send_retry_hooks.py` | | |
| `tests/gateway/test_send_voice_dispatch_signatures.py` | | |
| `tests/gateway/test_send_voice_reply_notify.py` | | |
| `tests/gateway/test_session.py` | | |
| `tests/gateway/test_session_boundary_security_state.py` | | |
| `tests/gateway/test_session_db_handle_sharing.py` | | |
| `tests/gateway/test_session_db_recovery.py` | | |
| `tests/gateway/test_session_env.py` | | |
| `tests/gateway/test_session_hygiene.py` | | |
| `tests/gateway/test_session_hygiene_turnhold_adoption.py` | | |
| `tests/gateway/test_session_identity.py` | | |
| `tests/gateway/test_session_identity_restore.py` | | |
| `tests/gateway/test_session_info.py` | | |
| `tests/gateway/test_session_load_bool.py` | | |
| `tests/gateway/test_session_model_override_credential_pool.py` | | |
| `tests/gateway/test_session_model_override_persistence.py` | | |
| `tests/gateway/test_session_model_override_routing.py` | | |
| `tests/gateway/test_session_model_reset.py` | | |
| `tests/gateway/test_session_override_thread_recovery.py` | | |
| `tests/gateway/test_session_race_guard.py` | | |
| `tests/gateway/test_session_recovery.py` | | |
| `tests/gateway/test_session_reset_notify.py` | | |
| `tests/gateway/test_session_split_brain.py` | | |
| `tests/gateway/test_session_stall_watchdog.py` | | |
| `tests/gateway/test_session_state_cleanup.py` | | |
| `tests/gateway/test_session_store_lock_io.py` | | |
| `tests/gateway/test_session_store_prune.py` | | |
| `tests/gateway/test_session_store_runtime_stale_guard.py` | | |
| `tests/gateway/test_session_time_persistence.py` | | |
| `tests/gateway/test_session_title_rename_lane.py` | | |
| `tests/gateway/test_shared_group_sender_prefix.py` | | |
| `tests/gateway/test_shutdown_cache_cleanup.py` | | |
| `tests/gateway/test_shutdown_executor_quiesce.py` | | |
| `tests/gateway/test_shutdown_flush_recovery.py` | | |
| `tests/gateway/test_shutdown_memory_provider_messages.py` | | |
| `tests/gateway/test_shutdown_teardown_profile_scope.py` | | |
| `tests/gateway/test_silent_partial_delivery.py` | | |
| `tests/gateway/test_skill_slash_offloop.py` | | |
| `tests/gateway/test_skip_context_files_wiring.py` | | |
| `tests/gateway/test_slack.py` | | |
| `tests/gateway/test_slack_bot_auth_bypass.py` | | |
| `tests/gateway/test_slack_channel_session_scope.py` | | |
| `tests/gateway/test_slack_channel_skills.py` | | |
| `tests/gateway/test_slack_clarify_buttons.py` | | |
| `tests/gateway/test_slack_download_ssrf.py` | | |
| `tests/gateway/test_slack_ignore_other_user_mentions.py` | | |
| `tests/gateway/test_slack_model_picker.py` | | |
| `tests/gateway/test_slack_native_streaming.py` | | |
| `tests/gateway/test_slack_require_mention_channels.py` | | |
| `tests/gateway/test_slack_runner_ignored_channels.py` | | |
| `tests/gateway/test_slack_turn_recipient_identity.py` | | |
| `tests/gateway/test_slash_access.py` | | |
| `tests/gateway/test_slash_access_dispatch.py` | | |
| `tests/gateway/test_slash_config_writes_routed_profile.py` | | |
| `tests/gateway/test_slash_dispatch_routed_scope.py` | | |
| `tests/gateway/test_split_final_suffix_reconcile.py` | | |
| `tests/gateway/test_stale_base_url_api_mode.py` | | |
| `tests/gateway/test_stale_confirmation_expiry.py` | | |
| `tests/gateway/test_stale_finalize_suppression.py` | | |
| `tests/gateway/test_stale_platform_lock_retryable.py` | | |
| `tests/gateway/test_standalone_gateway_launch_scope.py` | | |
| `tests/gateway/test_standalone_injected_config.py` | | |
| `tests/gateway/test_startup_connect_parallel.py` | | |
| `tests/gateway/test_startup_failed_platform_reports_degraded.py` | | |
| `tests/gateway/test_startup_restart_race.py` | | |
| `tests/gateway/test_startup_warmup_profile_scope.py` | | |
| `tests/gateway/test_status.py` | | |
| `tests/gateway/test_status_command.py` | | |
| `tests/gateway/test_status_free_tier_line.py` | | |
| `tests/gateway/test_stderr_formatting.py` | | |
| `tests/gateway/test_steer_command.py` | | |
| `tests/gateway/test_steer_fifo_overwrite.py` | | |
| `tests/gateway/test_stop_chat_scope.py` | | |
| `tests/gateway/test_stop_ends_background_delegations.py` | | |
| `tests/gateway/test_stop_thread_sibling.py` | | |
| `tests/gateway/test_stream_abandon_on_turn_death.py` | | |
| `tests/gateway/test_stream_consumer.py` | | |
| `tests/gateway/test_stream_consumer_continuation_cut.py` | | |
| `tests/gateway/test_stream_consumer_draft.py` | | |
| `tests/gateway/test_stream_consumer_fresh_final.py` | | |
| `tests/gateway/test_stream_consumer_hot_reload.py` | | |
| `tests/gateway/test_stream_consumer_oversized_leftover.py` | | |
| `tests/gateway/test_stream_consumer_silence.py` | | |
| `tests/gateway/test_stream_consumer_thread_routing.py` | | |
| `tests/gateway/test_stream_edit_flood_retry_after.py` | | |
| `tests/gateway/test_stream_events.py` | | |
| `tests/gateway/test_stream_final_contract.py` | | |
| `tests/gateway/test_streaming_tts_consumer.py` | | |
| `tests/gateway/test_streaming_tts_gateway_regression.py` | | |
| `tests/gateway/test_stt_config.py` | | |
| `tests/gateway/test_stt_transcript_echo_config.py` | | |
| `tests/gateway/test_stuck_loop.py` | | |
| `tests/gateway/test_subagent_protection.py` | | |
| `tests/gateway/test_suppression_contract_matrix.py` | | |
| `tests/gateway/test_systemd_watchdog_lifecycle.py` | | |
| `tests/gateway/test_teams.py` | | |
| `tests/gateway/test_teams_pipeline_runtime_wiring.py` | | |
| `tests/gateway/test_telegram_audio_vs_voice.py` | | |
| `tests/gateway/test_telegram_bot_auth_bypass.py` | | |
| `tests/gateway/test_telegram_clarify_buttons.py` | | |
| `tests/gateway/test_telegram_documents.py` | | |
| `tests/gateway/test_telegram_error_redaction.py` | | |
| `tests/gateway/test_telegram_final_delivery.py` | | |
| `tests/gateway/test_telegram_group_gating.py` | | |
| `tests/gateway/test_telegram_image_precompress.py` | | |
| `tests/gateway/test_telegram_inbound_warning_owner.py` | | |
| `tests/gateway/test_telegram_ingress_delivery_gap.py` | | |
| `tests/gateway/test_telegram_mention_context.py` | | |
| `tests/gateway/test_telegram_model_picker.py` | | |
| `tests/gateway/test_telegram_network_reconnect.py` | | |
| `tests/gateway/test_telegram_overflow_partial.py` | | |
| `tests/gateway/test_telegram_photo_interrupts.py` | | |
| `tests/gateway/test_telegram_prune_stale_topic_binding.py` | | |
| `tests/gateway/test_telegram_reactions.py` | | |
| `tests/gateway/test_telegram_rich_messages.py` | | |
| `tests/gateway/test_telegram_shared_outbound_budget.py` | | |
| `tests/gateway/test_telegram_split_send_flood.py` | | |
| `tests/gateway/test_telegram_status_update.py` | | |
| `tests/gateway/test_telegram_thread_fallback.py` | | |
| `tests/gateway/test_telegram_topic_mode.py` | | |
| `tests/gateway/test_telegram_topic_profile_routing.py` | | |
| `tests/gateway/test_telegram_voice_v0_regressions.py` | | |
| `tests/gateway/test_text_batch_flush_invariants.py` | | |
| `tests/gateway/test_text_batching.py` | | |
| `tests/gateway/test_title_command.py` | | |
| `tests/gateway/test_tool_log_mode.py` | | |
| `tests/gateway/test_tool_response_drop_recovery.py` | | |
| `tests/gateway/test_transcript_offset.py` | | |
| `tests/gateway/test_transcript_read_failure.py` | | |
| `tests/gateway/test_tts_media_routing.py` | | |
| `tests/gateway/test_turn_context.py` | | |
| `tests/gateway/test_turn_lease.py` | | |
| `tests/gateway/test_turn_pool_unbounded.py` | | |
| `tests/gateway/test_turn_request_overrides.py` | | |
| `tests/gateway/test_typing_indicator_toggle.py` | | |
| `tests/gateway/test_unauthorized_dm_behavior.py` | | |
| `tests/gateway/test_unauthorized_sender_notices.py` | | |
| `tests/gateway/test_undo_rewind_session.py` | | |
| `tests/gateway/test_unknown_command.py` | | |
| `tests/gateway/test_update_command.py` | | |
| `tests/gateway/test_update_streaming.py` | | |
| `tests/gateway/test_verbose_command.py` | | |
| `tests/gateway/test_video_context_note.py` | | |
| `tests/gateway/test_vision_memory_leak.py` | | |
| `tests/gateway/test_vision_preprocess.py` | | |
| `tests/gateway/test_voice_command.py` | | |
| `tests/gateway/test_voice_mode_platform_isolation.py` | | |
| `tests/gateway/test_voice_transcode.py` | | |
| `tests/gateway/test_voice_transcript_dedup_scope.py` | | |
| `tests/gateway/test_warning_delivery_real_producers.py` | | |
| `tests/gateway/test_warning_emission_boundary.py` | | |
| `tests/gateway/test_warning_notifications.py` | | |
| `tests/gateway/test_warning_notifications_transport.py` | | |
| `tests/gateway/test_warning_wiring_conservation.py` | | |
| `tests/gateway/test_watchdog_review.py` | | |
| `tests/gateway/test_webhook_deliver_only.py` | | |
| `tests/gateway/test_webhook_integration.py` | | |
| `tests/gateway/test_webhook_profile_egress.py` | | |
| `tests/gateway/test_webhook_route_toolsets.py` | | |
| `tests/gateway/test_webhook_session_close.py` | | |
| `tests/gateway/test_whatsapp_connect.py` | | |
| `tests/gateway/test_whatsapp_multiplex.py` | | |
| `tests/gateway/test_whatsapp_stale_bridge.py` | | |
| `tests/gateway/test_whatsapp_text_batching.py` | | |
| `tests/gateway/test_yolo_command.py` | | |
| `tests/hermes_cli/test_browser_connect_media_permissions.py` | | |
| `tests/hermes_cli/test_gateway_multiplex_mode.py` | | |
| `tests/hermes_cli/test_gateway_run_hard_exit.py` | | |
| `tests/hermes_cli/test_kanban_notify.py` | | |
| `tests/hermes_cli/test_logs.py` | | |
| `tests/hermes_cli/test_pairing.py` | | |
| `tests/hermes_cli/test_platform_actions.py` | | |
| `tests/hermes_cli/test_plugins_cadence.py` | | |
| `tests/hermes_cli/test_pre_command_hook.py` | | |
| `tests/hermes_cli/test_quick_commands.py` | | |
| `tests/hermes_cli/test_save_transcript_history.py` | | |
| `tests/hermes_cli/test_session_end_messages.py` | | |
| `tests/hermes_cli/test_status_report_contract.py` | | |
| `tests/plugins/platforms/telegram/test_allowlist_json_adapter.py` | | |
| `tests/pm/test_startup_activation.py` | | |
| `tests/tools/test_async_delegation_orphan_sweep.py` | | |
| `tests/tools/test_audio_container.py` | | |
| `tests/tools/test_mcp_resource_content.py` | | |
| `tests/tools/test_send_message_cross_loop.py` | | |
| `tests/tools/test_send_message_multiplex_profile_adapter.py` | | |
| `tests/tools/test_send_message_react.py` | | |
| `tests/tools/test_send_message_relay_target_authz.py` | | |
| `tests/tools/test_send_message_target_parse.py` | | |
| `tests/tools/test_send_message_telegram_proxy.py` | | |
| `tests/tools/test_send_message_tool.py` | | |
| `tests/tools/test_telegram_send_message_caption.py` | | |
| `tests/tools/test_terminal_scope_multiplex.py` | | |
| `tests/tools/test_terminal_watcher_arming.py` | | |
| `tests/tools/test_tts_path_traversal.py` | | |
| `tests/tools/test_tts_prepare_spoken.py` | | |
| `tests/tools/test_tts_text_normalize.py` | | |
| `tests/tui_gateway/test_reasoning_config_per_model.py` | | |
| `tests/tui_gateway/test_served_profile_child_env.py` | | |

### telegram (54 files, floor 60.3%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/gateway/test_allowed_channels_widening.py` | | |
| `tests/gateway/test_dm_topics.py` | | |
| `tests/gateway/test_late_plugin_rewire.py` | | |
| `tests/gateway/test_multiplex_endpoint_identity_scope.py` | | |
| `tests/gateway/test_native_warning_coverage.py` | | |
| `tests/gateway/test_platform_plugin_handlers.py` | | |
| `tests/gateway/test_telegram_auth_check.py` | | |
| `tests/gateway/test_telegram_callback_auth_fail_closed.py` | | |
| `tests/gateway/test_telegram_caption_merge.py` | | |
| `tests/gateway/test_telegram_channel_posts.py` | | |
| `tests/gateway/test_telegram_closewait_limits.py` | | |
| `tests/gateway/test_telegram_cold_boot_queue.py` | | |
| `tests/gateway/test_telegram_conflict.py` | | |
| `tests/gateway/test_telegram_connect.py` | | |
| `tests/gateway/test_telegram_fallback_pool_release.py` | | |
| `tests/gateway/test_telegram_format.py` | | |
| `tests/gateway/test_telegram_forum_commands.py` | | |
| `tests/gateway/test_telegram_init_deadline.py` | | |
| `tests/gateway/test_telegram_inline_picker.py` | | |
| `tests/gateway/test_telegram_lazy_install_typehandler.py` | | |
| `tests/gateway/test_telegram_long_command_batching.py` | | |
| `tests/gateway/test_telegram_max_doc_bytes.py` | | |
| `tests/gateway/test_telegram_media_read_timeout.py` | | |
| `tests/gateway/test_telegram_mention_boundaries.py` | | |
| `tests/gateway/test_telegram_network.py` | | |
| `tests/gateway/test_telegram_pending_update_probe.py` | | |
| `tests/gateway/test_telegram_polling_health_confirmation.py` | | |
| `tests/gateway/test_telegram_polling_progress.py` | | |
| `tests/gateway/test_telegram_polling_stall_watchdog.py` | | |
| `tests/gateway/test_telegram_reply_mode.py` | | |
| `tests/gateway/test_telegram_reply_quote.py` | | |
| `tests/gateway/test_telegram_rich_newlines.py` | | |
| `tests/gateway/test_telegram_send_draft_format.py` | | |
| `tests/gateway/test_telegram_send_path_health.py` | | |
| `tests/gateway/test_telegram_send_reconnect_wait.py` | | |
| `tests/gateway/test_telegram_slash_confirm.py` | | |
| `tests/gateway/test_telegram_start_polling_timeout.py` | | |
| `tests/gateway/test_telegram_status_indicator.py` | | |
| `tests/gateway/test_telegram_text_batch_perf.py` | | |
| `tests/gateway/test_telegram_text_batching.py` | | |
| `tests/gateway/test_telegram_text_link_expansion.py` | | |
| `tests/gateway/test_telegram_typing_backoff.py` | | |
| `tests/gateway/test_telegram_typing_retrigger.py` | | |
| `tests/gateway/test_telegram_username_chat_id.py` | | |
| `tests/gateway/test_telegram_video_metadata.py` | | |
| `tests/gateway/test_telegram_voice_caption_markdown.py` | | |
| `tests/gateway/test_telegram_voice_duration.py` | | |
| `tests/gateway/test_telegram_webhook_secret.py` | | |
| `tests/plugins/platforms/telegram/test_inline_picker_offset.py` | | |
| `tests/plugins/platforms/telegram/test_notification_kwargs.py` | | |
| `tests/plugins/platforms/test_discord_gate_isolation.py` | | |
| `tests/plugins/test_telegram_polling_progress_ptb.py` | | |
| `tests/plugins/test_telegram_update_admission.py` | | |
| `tests/tools/test_telegram_send_message_video_metadata.py` | | |

### discord (36 files, floor 54.2%)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/e2e/core/platforms/test_discord_thread_rename_ordering.py` | | |
| `tests/e2e/test_discord_adapter.py` | | |
| `tests/gateway/test_adapter_settings_profile_parity.py` | | |
| `tests/gateway/test_discord_admission_thread_exemption.py` | | |
| `tests/gateway/test_discord_allow_bots_yaml_config.py` | | |
| `tests/gateway/test_discord_allowed_mentions.py` | | |
| `tests/gateway/test_discord_approval_mentions.py` | | |
| `tests/gateway/test_discord_attachment_download.py` | | |
| `tests/gateway/test_discord_attachment_receipts.py` | | |
| `tests/gateway/test_discord_bot_inline_mentions.py` | | |
| `tests/gateway/test_discord_channel_controls.py` | | |
| `tests/gateway/test_discord_channel_skills.py` | | |
| `tests/gateway/test_discord_component_auth.py` | | |
| `tests/gateway/test_discord_connect.py` | | |
| `tests/gateway/test_discord_double_dispatch.py` | | |
| `tests/gateway/test_discord_edit_message_overflow.py` | | |
| `tests/gateway/test_discord_exec_approval_content.py` | | |
| `tests/gateway/test_discord_format.py` | | |
| `tests/gateway/test_discord_free_response.py` | | |
| `tests/gateway/test_discord_home_link.py` | | |
| `tests/gateway/test_discord_imports.py` | | |
| `tests/gateway/test_discord_liveness.py` | | |
| `tests/gateway/test_discord_missed_message_backfill.py` | | |
| `tests/gateway/test_discord_prompt_content_siblings.py` | | |
| `tests/gateway/test_discord_race_polish.py` | | |
| `tests/gateway/test_discord_reply_mode.py` | | |
| `tests/gateway/test_discord_send.py` | | |
| `tests/gateway/test_discord_split_cap.py` | | |
| `tests/gateway/test_discord_sync_limit.py` | | |
| `tests/gateway/test_discord_thread_persistence.py` | | |
| `tests/gateway/test_discord_thread_slash_expired_defer.py` | | |
| `tests/gateway/test_discord_voice_mixer.py` | | |
| `tests/gateway/test_discord_voice_rekey.py` | | |
| `tests/plugins/platforms/test_bracket_list_allowlists.py` | | |
| `tests/plugins/test_discord_runtime_failure.py` | | |
| `tests/tools/test_discord_send_message_caption.py` | | |

### threats (added in task 2.2b1: tools/threat_patterns.py was not in the original module list)

| File | Bucket | Go counterpart / reason |
|---|---|---|
| `tests/tools/test_threat_patterns.py` | behaviour | `internal/threats` TestScanMatchesPython, TestFirstThreatMessageMatchesPython (76 corpus cases run through Python's real `scan_for_threats` in all three scopes), TestScopesAreCumulative, TestScanCapsTheScannedText, TestBoundComposesLongRepeats, FuzzScanNeverPanics |
| `tests/tools/test_translate_execute_context.py` | behaviour | subset: `internal/threats` TestScanMatchesPython translate_execute case. The code-execution translation tool itself is dropped |
| `tests/tools/test_cron_prompt_injection.py` | later | cron prompt scanning uses `threats.Scan`: re-bucket in task 6.1 |
