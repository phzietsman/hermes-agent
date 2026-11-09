"""Task 8.1: bucket the ledger rows left unresolved by name and directory (see the Resolution status note in PORT_TESTS.md).

usage: python scripts/port/ledger_sweep.py   (run from the repo root; rewrites PORT_TESTS.md in place)
"""
import re
p='PORT_TESTS.md'
L=open(p).read().split('\n')

GW_NOT = 'gateway machinery beyond the lean Stage 1 gateway (G31): see the decision log for what is and is not ported'
def rules():
    B=lambda pat,cp,scope=None: (re.compile(pat),'behaviour',cp,scope)
    D=lambda pat,why: (re.compile(pat),'dropped',why,None)
    return [
     # ---- behaviour: where a Go test covers the contract ----
     B(r'pairing|allowlist|allowed_user|allowed_channel|authoriz|authz|unauthori|access_control', 'gateway: TestStrangersGetAPairingCodeAndTheOwnerApproves, TestPairingCapAndExpiry, TestGroupsStaySilentForStrangersAndUnaddressedMessages', r'^tests/(gateway|plugins)/'),
     B(r'stream_consumer|stream_final|draft_stream|edit_message|streaming_|stream_edit|progressive|flood_control', 'gateway: TestStreamsAReplyByEditingOneMessage, TestEditFailureFallsBackToANewMessage', r'^tests/gateway/'),
     B(r'split_cap|message_split|long_message|chunk|truncate_message|overflow_split|edit_message_overflow|[^a-z]text_length|[^a-z]max_length', 'gateway: TestLongRepliesContinueInANewMessage, TestSplitTextPrefersBreaks', r'^tests/(gateway|agent)/'),
     B(r'typing', 'gateway: TestStreamsAReplyByEditingOneMessage (typing indicator); telegram/discord: TestSendEditTypingWireShape, TestSendEditTypingGoOutWithoutPings', r'^tests/gateway/'),
     B(r'unknown_command|slash_command_help|help_command', 'gateway: TestStopAndHelpWhenIdleAndUnknownSlashGoToTheAgent', r'^tests/gateway/'),
     B(r'busy|active_session|queue|stop_command|interrupt_command|/stop', 'gateway: TestBusyChatQueuesInOrderAndStopInterrupts, TestQueueFullIsSaidOutLoud', r'^tests/gateway/'),
     B(r'approval_(prompt|ui|timeout|choice)|approve_command|_approve', 'gateway: TestDangerousCommandsAreApprovedInTheChat', r'^tests/(gateway|hermes_cli|tools)/'),
     B(r'session_reset|auto_reset|new_session|resume|session_key|session_routing|session_store_key', 'gateway: TestNewStartsAFreshSessionAndRestartResumesTheOldOne', r'^tests/gateway/'),
     B(r'idle_reset|session_idle|idle_expir|reaper', 'gateway: TestIdleSessionsEndAndResume', r'^tests/gateway/'),
     B(r'shutdown|graceful_stop', 'gateway: TestShutdownEndsSessionsAndStopsAdapters', r'^tests/gateway/'),
     B(r'cron_delivery_target|delivery_target|home_channel|deliver_origin', 'gateway: TestCronDeliveryTargets, TestAgentCreatedCronJobsAddressTheirChat', r'^tests/gateway/'),
     B(r'group_mention|require_mention|reply_to_bot|inline_mention|bot_mention', 'telegram: TestGroupMessagesAreAddressedByMentionOrReplyOnly; discord: TestDMsAndMentionsAreAddressedAndEverythingElseIsNot', r'^tests/gateway/'),
     B(r'forum|topic|thread_id|message_thread', 'telegram: TestOtherBotsEditsAndNonTextAreIgnoredAndTopicsCarryThreads, TestSendEditTypingWireShape', r'^tests/gateway/'),
     B(r'allowed_mentions|no_ping|approval_mentions|everyone', 'discord: TestSendEditTypingGoOutWithoutPings', r'^tests/gateway/'),
     B(r'rate_limit|retry_after|flood_wait|429', 'telegram: TestRateLimitIsWaitedOutOnce', r'^tests/gateway/'),
     B(r'poll(ing)?_(stall|restart|recover|conflict)|reconnect|polling_error|start_polling|conflict', 'telegram: TestPollingSurvivesFailuresAndStops; discord: discordgo reconnects; Start/Stop tests', r'^tests/gateway/'),
     B(r'discord_connect|discord_login|discord_start|ready_event|identify', 'discord: TestStartIdentifiesWithTheRightTokenAndIntents, TestStartGivesUpWhenReadyNeverComes', r'^tests/gateway/'),
     B(r'e2e/test_discord_adapter', 'discord: TestADiscordChatEndToEnd'),
     B(r'telegram_send_path|telegram_send|discord_send\b|test_discord_send|send_error', 'telegram: TestErrorsAreReportedAndNotModifiedIsFine; discord: TestFailuresAreReportedWithoutTheToken', r'^tests/gateway/'),
     B(r'token_redact|secret_leak|token_scrub|redact_token', 'telegram: TestStartRejectsABadTokenWithoutLeakingIt; discord: TestFailuresAreReportedWithoutTheToken', r'^tests/gateway/'),
     B(r'lifecycle|restart_service|gateway_restart|self_kill|pkill', 'lifecycle: TestCheck; shell: TestGatewayLifecycleCommandsAreRefusedInsideAGatewayOnly', r'^tests/(gateway|agent|cron|tools)/'),
     B(r'env_loader|dotenv|env_file|load_env', 'config: dotenv tests (TestParseEnv..., TestSecrets...)', r'^tests/(agent|hermes_cli)/'),
     B(r'^tests/(hermes_cli/)?test_profiles?\.py', 'config: home and profile resolution tests'),
     B(r'file_read_guards|read_file|write_file|file_tools|file_state|patch_|v4a|fuzzy|file_operations', 'filetools/fuzzy: files, patch and V4A tests', r'^tests/tools/'),
     B(r'cron_prompt_injection|cronjob_prompt|cron_prompt_scan', 'cron: TestScanPrompt, TestCreateRejections', r'^tests/(tools|cron)/'),
     B(r'search_budget|result_budget|tool_result_limit|max_result', 'tools: limitResult tests (registry)', r'^tests/tools/'),
     B(r'delegat|subagent_lifecycle|finite_delegation', 'delegate: TestChildrenRunInOwnSessionsAndReturnSummaries, TestChildSelectionBlocksDelegationAndMemory', r'^tests/(tools|agent)/'),
     B(r'empty_response|empty_reply|truncat|partial_stream|continuation|length_finish|invalid_tool|tool_call_repair|repair_tool|bad_json|unknown_tool', 'agent: loop, truncation and tool-call repair tests (loop_test.go, truncation_test.go, toolargs_test.go)', r'^tests/agent/'),
     B(r'context_overflow|over_window|overflow|preflight_compress', 'agent: overflow and compaction retry tests', r'^tests/agent/'),
     B(r'interrupt|sequential_tool_interrupt|cancel', 'agent: interrupt tests (TestInterrupted..., tool cancellation)', r'^tests/agent/'),
     B(r'prompt_cache|cache_control|cache_breakpoint|prefix_contract', 'agent/cmd: prompt-cache tests (TestChatAgainstAFakeAnthropicServer, cache plan tests)', r'^tests/agent/'),
     B(r'compression|compaction|pre_compress', 'agent: compaction tests (compactor_test.go, compaction_acceptance_test.go)', r'^tests/(agent|gateway)/'),
     # ---- dropped, by family ----
     D(r'tts|voice|speech|audio|whisper|stt|transcri', 'voice, speech and audio are not in Stage 1'),
     D(r'video|photo|image|media|attachment|vision|screenshot|sticker|gif|document_|pdf|ocr|camera', 'media and vision are not in Stage 1'),
     D(r'browser|playwright|cdp|selenium|computer_use|desktop_|web_ui|dashboard|webui|electron|tui|ink', 'browser, computer use and UI frontends are out of scope (TypeScript frontends, no browser stack in Stage 1)'),
     D(r'mcp', 'MCP servers are not in Stage 1'),
     D(r'codex|gemini|openai|ollama|bedrock|vertex|openrouter|nous|copilot|qwen|deepseek|grok|xai|kimi|mistral|azure|groq|cerebras|zai|minimax|glm|llama|lmstudio|local_model|provider_|credential_pool|fallback_|model_catalog|model_switch|models_dev|auxiliary|aux_', 'other model providers, credential pools, fallbacks and auxiliary models are not in Stage 1 (Anthropic only)'),
     D(r'whatsapp|slack|matrix|signal|email|sms|imessage|bluebubbles|homeassistant|teams|mattermost|feishu|lark|dingtalk|wecom|weixin|line_|irc|webex|zulip|twitch|facebook|instagram|messenger|rocket|revolt|nostr|xmpp|bot_chat|botmode|relay|kanban|moa_|mixture|spotify|meet_|image_gen|video_gen|achievements|plugin_|plugins/|curator|lsp|notebook|execute_code|code_exec|checkpoint|rollback|worktree', 'platforms, tools and plugin families outside Stage 1: Telegram and Discord only, no kanban, MoA, LSP, code execution, notebooks, checkpoints or plugins'),
     D(r'multiplex|profile_scope|profile_route|profile_env|served_profile|profile_isolation|host_gateway|shared_ingress|profile_parity|secret_scope', 'multiplexed profiles are not in Stage 1: one process serves one profile'),
     D(r'webhook|api_server|apiserver|fire_|drain|scale_to_zero|handoff|hosted|completion_delivery|notifier|wake|heartbeat|status_indicator|startup_warmup|warmup|restart_drain|update_|updater|self_update', 'gateway entry points, drains and hosting machinery beyond the lean gateway (G31)'),
     D(r'hermes_state/|state_synchronous|sqlite|wal|fts|db_|_db|session_db|state_db|degraded_db|trigram|cjk|repair|lineage|lease|watermark|rewind|sidecar|backfill|api_content|display_order|message_uid|usage_anchor|token_accounting|billing|cost_|pricing|insights|usage_', 'Python session-store, billing and accounting internals (leases, lineage, sidecars, repair, display order) are not ported; the Go store has its own tests'),
     D(r'setup|wizard|onboard|doctor|install|uninstall|migrat|upgrade|version_check|banner|skin|theme|prompt_toolkit|completer|autocomplete|display_|spinner|rich_|ansi|color|format_|markdown|html_|escape|entity|link_expansion', 'CLI setup, display and formatting layers beyond the Stage 1 CLI (plain text, no skins or wizards)'),
     D(r'reconnect|watchdog|stall|deadline|timeout_|init_|connect_|closewait|cold_boot|fallback_pool|read_timeout|send_timeout|health|keepalive|ping|rewire|late_plugin|platform_reconnect', 'transport hardening of the Python adapters (watchdogs, stall detection, cold-boot queues); the Go adapters rely on the platform libraries and tested backoff'),
     D(r'callback|inline|picker|keyboard|button|component|modal|slash_|command_menu|menu|reaction|emoji|poll_|voice_', 'interactive UI surfaces (buttons, pickers, components, reactions) are not in Stage 1; commands are text'),
     D(r'steer|goal|reasoning|thinking|effort|fast_mode|service_tier|planner|todo|memory_flush|nudge|skill_applied|skill_invocation|skill_collision|reload_skills|oneshot|preload|secret_capture', 'features beyond Stage 1: steering, goals, reasoning controls, todo planner, skill notifications and reload'),
     D(r'guardrail|runtime_self_protection|self_protect|authorization_gate|turn_author|turn_context|turn_finalizer|turn_recovery|recovery_diagnostic|detached_fork|replay_budget|issuer_attribution|merged_user_rows|repair_reanchors|in_place|relay_atof|nous_rate|soft_failure|pool_rotation|native_preflight|gateway_turn_sidecar|sanitiser_escalation|hygiene|coalesce|preserve', 'Python turn-loop internals with no Go counterpart (turn-context objects, recovery producers, replay accounting, rotation); the Go loop is a different, smaller design (G16 to G19)'),
    ]
_all=rules()
_strong=[r for r in _all if r[1]=='dropped' and r[2].startswith(('voice','media','browser','MCP','other model','platforms','multiplexed','features beyond'))]
RULES=_strong+[r for r in _all if r not in _strong]
DEFAULT={
 'gateway-core':('dropped',GW_NOT),'telegram':('dropped','Telegram feature beyond the Stage 1 adapter (G32): formatting, media, polling hardening and UI'),
 'discord':('dropped','Discord feature beyond the Stage 1 adapter (G33): voice, attachments, slash UI and channel controls'),
 'config':('dropped','Python configuration, loader and CLI machinery beyond the Stage 1 config (G4): the Go config has its own tests'),
 'session':('dropped','Python session-store, gateway and agent persistence internals beyond the Stage 1 store (G5)'),
 'loop':('dropped','Python turn-loop internals with no Go counterpart; the Go loop is a different, smaller design (G16 to G19)'),
 'tools-core':('dropped','tool family or tool-runtime machinery outside Stage 1 (see G22 to G24)'),
}
SPECIAL={
 'tests/hermes_cli/test_auth_commands.py':('dropped','auth commands and credential pools are not in Stage 1; credentials come from .env or an adopted Claude Code login'),
 'tests/agent/test_subdirectory_hints.py':('dropped','subdirectory context hints are not ported (project context files load from the working directory only)'),
 'tests/gateway/test_hosted_room_discussion.py':('dropped','hosted rooms are not in Stage 1'),
 'tests/agent/test_conversation_compression_manual.py':('dropped','manual /compress is not in Stage 1; compaction is automatic'),
 'tests/hermes_cli/test_cli_manual_compress.py':('dropped','manual /compress is not in Stage 1; compaction is automatic'),
 'tests/agent/test_cron_inflight_prompt_reappend.py':('dropped','the cron in-flight prompt re-append belongs to Python cron workers (G30)'),
 'tests/agent/test_pre_compress_checkpoint_contract.py':('behaviour','memory: TestTranscriptIsRememberedOnceAcrossPreCompressAndSessionEnd; agent: TestPreCompressSeesTheTranscriptBeforeItIsReplaced'),
 'tests/agent/test_pre_compress_memory_context.py':('dropped','memory context handed to the summarizer is not ported (G19, G27): the transcript is queued to memory before compaction instead'),
 'tests/tools/test_execute_code_approval_cluster.py':('dropped','the code-execution tool is not in Stage 1'),
 'tests/hermes_cli/test_cli_yolo_toggle.py':('behaviour','cmd/hermes: TestOnceApprovesAndYoloSkipsThePromptHardlineBlocksEvenWithYolo (/yolo and --yolo)'),
 'tests/agent/test_runtime_self_protection.py':('behaviour','lifecycle/shell: the gateway-lifecycle guard; approval: hardline blocks'),
 'tests/cron/test_scheduler_cron_session_isolation.py':('behaviour','app: TestCronRunExecutesAJobInAReadOnlySessionWithoutTheCronTool'),
 'tests/tools/test_cron_prompt_injection.py':('behaviour','cron: TestScanPrompt, TestCreateRejections (injection)'),
 'tests/hermes_cli/test_commands.py':('behaviour','cmd/hermes: TestReplHandlesSlashCommandsAndSkills (slash commands and skill commands)'),
 'tests/hermes_cli/test_oneshot_skills.py':('dropped','one-shot skill preloading flags are not in Stage 1; /<skill> loads a skill in the REPL'),
 'tests/hermes_cli/test_cli_reload_skills.py':('dropped','reloading skills mid-session would break the cached prefix (agent invariant); skills load at session start'),
 'tests/hermes_cli/test_skill_collision_visibility.py':('behaviour','skills: catalog resolution tests (unique, ambiguous, shadowed) and commands tests'),
 'tests/hermes_cli/test_cli_secret_capture.py':('dropped','skill secret capture prompts are not in Stage 1'),
 'tests/hermes_cli/test_discord_skill_clamp_warning.py':('dropped','Discord slash-command clamping does not apply: skills are not registered as slash commands'),
 'tests/agent/test_skill_invocation_description.py':('dropped','skill invocation descriptions for slash menus are not in Stage 1'),
 'tests/agent/test_read_dedup_rewrite_boundaries.py':('dropped','read de-duplication stubs are not ported (the file tools return full reads)'),
 'tests/agent/test_skill_applied_notifications.py':('dropped','skill-applied notifications are not in Stage 1'),
}
def group_of(i):
    for j in range(i,-1,-1):
        if L[j].startswith('### '): return L[j][4:].split(' (')[0]
    return ''
n=0; cnt={'behaviour':0,'dropped':0}
for i,l in enumerate(L):
    m=re.match(r'\| `([^`]+)` \| (later|) *\|',l)
    if not m: continue
    blank = l.rstrip().endswith('| | |')
    if not (m.group(2)=='later' or blank): continue
    path=m.group(1); key=path.lower()
    if path in SPECIAL: b,r=SPECIAL[path]
    else:
        b=r=None
        for pat,bk,rs,sc in RULES:
            if pat.search(key) and (sc is None or re.search(sc,key)): b,r=bk,rs; break
        if b is None: b,r=DEFAULT.get(group_of(i),('dropped','Python machinery outside Stage 1'))
    L[i]=f'| `{path}` | {b} | {r} |'; n+=1; cnt[b]+=1
open(p,'w').write('\n'.join(L)); print(n,cnt)
