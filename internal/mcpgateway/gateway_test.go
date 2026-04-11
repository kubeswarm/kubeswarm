package mcpgateway

import (
	"testing"
)

func TestParsePath(t *testing.T) {
	cases := []struct {
		path      string
		wantNS    string
		wantAgent string
		wantEP    string
		wantOK    bool
	}{
		{"/namespaces/ai-platform/agents/database-agent/sse", "ai-platform", "database-agent", "sse", true},
		{"/namespaces/ai-platform/agents/database-agent/message", "ai-platform", "database-agent", "message", true},
		{"/namespaces/ai-platform/agents/database-agent/tools/list", "ai-platform", "database-agent", "tools/list", true},
		{"/namespaces/ai-platform/agents/database-agent/tools/call", "ai-platform", "database-agent", "tools/call", true},
		{"/healthz", "", "", "", false},
		{"/namespaces/ns/agents/sse", "", "", "", false},
		{"/namespaces//agents/db/sse", "", "", "", false},
	}
	for _, c := range cases {
		ns, agent, ep, ok := parsePath(c.path)
		if ok != c.wantOK || ns != c.wantNS || agent != c.wantAgent || ep != c.wantEP {
			t.Errorf("parsePath(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.path, ns, agent, ep, ok, c.wantNS, c.wantAgent, c.wantEP, c.wantOK)
		}
	}
}

func TestGatewayURL(t *testing.T) {
	got := GatewayURL("http://kubeswarm-mcp-gateway.kubeswarm-system.svc:8093", "ai-platform", "database-agent")
	want := "http://kubeswarm-mcp-gateway.kubeswarm-system.svc:8093/namespaces/ai-platform/agents/database-agent"
	if got != want {
		t.Errorf("GatewayURL = %q, want %q", got, want)
	}
}
