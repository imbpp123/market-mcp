TUNNEL_PROFILE ?= market-mcp
TUNNEL_HEALTH_ADDR ?= 127.0.0.1:8083
MCP_URL ?= http://127.0.0.1:8082/mcp

.PHONY: tunnel-init tunnel-doctor tunnel-run tunnel-ready

tunnel-init:
	@test -n "$$TUNNEL_ID" || { echo "Set TUNNEL_ID to the ID from OpenAI Platform" >&2; exit 2; }
	tunnel-client init \
		--sample sample_mcp_remote_no_auth \
		--profile "$(TUNNEL_PROFILE)" \
		--tunnel-id "$$TUNNEL_ID" \
		--mcp-server-url "$(MCP_URL)" \
		--health-listen-addr "$(TUNNEL_HEALTH_ADDR)"

tunnel-doctor:
	@test -n "$$CONTROL_PLANE_API_KEY" || { echo "Set CONTROL_PLANE_API_KEY in your shell" >&2; exit 2; }
	tunnel-client doctor --profile "$(TUNNEL_PROFILE)" --explain

tunnel-run: tunnel-doctor
	tunnel-client run --profile "$(TUNNEL_PROFILE)"

tunnel-ready:
	curl --fail --silent --show-error "http://$(TUNNEL_HEALTH_ADDR)/readyz"
