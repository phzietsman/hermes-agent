package lifecycle

import "testing"

func TestCheck(t *testing.T) {
	blocked := []string{
		"hermes gateway restart", "hermes gateway stop", "hermes  gateway  uninstall", "echo hi; hermes gateway restart", "$(hermes gateway stop)",
		"launchctl kickstart -k gui/501/ai.hermes.gateway", "launchctl bootout gui/501 ai.hermes.gateway", "launchctl submit -l ai.hermes.gateway-x -- /tmp/x.sh",
		"systemctl restart hermes-gateway", "systemctl --user stop hermes-gateway.service", "sudo systemctl start hermes-gateway",
		"pkill -f 'hermes gateway'", "pkill hermes-gateway", "kill $(pgrep -f gateway | grep hermes)", "Stop-Process -Name gateway-hermes",
		"pkill -9 python3", "killall python", "pkill python3.12", "pkill -f python", "pkill -f 'python -m hermes_cli.main gateway'", "killall -9 python3 ; echo done",
	}
	for _, c := range blocked {
		if Check(c) == "" {
			t.Errorf("not blocked: %q", c)
		}
	}
	allowed := []string{
		"hermes gateway start", "hermes gateway status", "echo see /docs/hermes gateway restart-notes.md", "xhermes gateway restart", "ls", "rm file", "echo hermes gateway",
		"launchctl list", "systemctl restart nginx", "pkill chrome", "pkill -u alice chrome", "pkill agent-browser", "pkill -f 'python mt_add.py'", "kill 1234", "killall node",
		"skill gateway hermes", "Summarise the gateway news for hermes fans", "pkill -f hermes-polis/run.sh",
	}
	for _, c := range allowed {
		if msg := Check(c); msg != "" {
			t.Errorf("blocked: %q (%s)", c, msg)
		}
	}
}

func TestMessagesDifferByBranch(t *testing.T) {
	if Check("hermes gateway stop") != GatewayRejection || Check("pkill python") != HostInterpreterKillRejection {
		t.Error("each branch carries its own rejection")
	}
}
