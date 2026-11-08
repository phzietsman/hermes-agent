package main

import (
	"context"
	"os"
	"testing"
)

func TestUsageErrorsAreReportedBeforeAnyWork(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "TYPESAFE_API_KEY", "AGENT_MEMORY_API_KEY", "AGENT_MEMORY_ENDPOINT", "AGENT_MEMORY_CONTEXT", "AGENT_MEMORY_ID"} {
		t.Setenv(k, "")
	}
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer null.Close()
	for name, tc := range map[string]struct {
		args []string
		want int
	}{
		"no command":          {nil, 2},
		"unknown command":     {[]string{"bogus"}, 2},
		"memory without -out": {[]string{"memory"}, 2},
		"latency without out": {[]string{"latency"}, 2},
		"memory no creds":     {[]string{"memory", "-out", "x.json"}, 1},
		"latency no creds":    {[]string{"latency", "-out", "x.json"}, 1},
		"bad flag":            {[]string{"memory", "-nope"}, 2},
	} {
		if got := run(context.Background(), tc.args, null, null); got != tc.want {
			t.Errorf("%s: exit %d, want %d", name, got, tc.want)
		}
	}
}

func TestParseSeeds(t *testing.T) {
	if s, err := parseSeeds("1, 2,3"); err != nil || len(s) != 3 || s[1] != 2 {
		t.Errorf("%v %v", s, err)
	}
	if _, err := parseSeeds("1,x"); err == nil {
		t.Error("bad seed accepted")
	}
}
