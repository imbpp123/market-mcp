# Market MCP

Market MCP is a read-only Model Context Protocol adapter for [Market Data](https://github.com/imbpp123/market-data) and [Market Analyzer](https://github.com/imbpp123/market-analyzer). It uses their pinned Go gRPC clients. It does not collect exchange data, calculate indicators, place trades, or make strategy decisions.

## Requirements and build

- Go 1.27.1.
- Running Market Data gRPC on `127.0.0.1:9090` and Market Analyzer gRPC on `127.0.0.1:9091`, or other loopback addresses set through the environment.

```sh
go mod download
mkdir -p bin
go build -o ./bin/market-mcp ./cmd/market-mcp
```

The dependency versions are fixed in `go.mod` and `go.sum`. There are no local module replacements or copied Protobuf files.

## Run locally

The default transport is stdio. It is intended for a local MCP client that starts the process:

```sh
./bin/market-mcp
```

For Streamable HTTP, run:

```sh
MARKET_MCP_TRANSPORT=http ./bin/market-mcp
```

The HTTP endpoint is `http://127.0.0.1:8082/mcp`. All configured listener and upstream endpoints must use loopback IP addresses. This process has no authentication or TLS and cannot bind to a public interface. If Market Data or Analyzer run on another host, use a protected local forwarding channel to their gRPC ports.

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `MARKET_MCP_TRANSPORT` | `stdio` | `stdio` or `http` |
| `MARKET_MCP_HTTP_ADDR` | `127.0.0.1:8082` | HTTP listener, loopback only |
| `MARKET_DATA_ENDPOINT` | `127.0.0.1:9090` | Market Data gRPC endpoint, loopback only |
| `MARKET_ANALYZER_ENDPOINT` | `127.0.0.1:9091` | Analyzer gRPC endpoint, loopback only |
| `MARKET_MCP_TIMEOUT` | `30s` | Per-upstream-call timeout, also bounded by the client deadline |
| `MARKET_MCP_MAX_RESULT_BYTES` | `262144` | Maximum serialized tool result, from 1024 to 4194304 bytes |

The server handles SIGINT and SIGTERM. HTTP shutdown allows ten seconds for active requests to finish; stdio stops when its transport closes or the process is signaled.

## Run with Docker Compose

Compose pulls the Market Data and Market Analyzer release images and builds Market MCP. The Analyzer image uses [release `v1.1.0`](https://github.com/imbpp123/market-analyzer/releases/tag/v1.1.0) with a fixed digest. No local Analyzer checkout is needed. On macOS, enable **Host networking** in Docker Desktop settings before starting the stack; otherwise `/mcp` is not reachable from the host. Then run:

```sh
docker compose up --build -d
```

Compose starts Market Data, waits for its health check, starts Market Analyzer, waits for its health check, and then starts Market MCP. The MCP endpoint is `http://127.0.0.1:8082/mcp`. Market Data and Analyzer also bind their gRPC and operational HTTP listeners to loopback. All three containers use host networking because Market MCP accepts only loopback endpoints. Do not run another copy of either upstream service on the same host ports. See [Docker's host networking guide](https://docs.docker.com/engine/network/drivers/host/). Host networking gives these containers access to host network services; use this Compose file only on a trusted host. Market Data uses memory storage, so its candle history is lost when the stack stops.

```sh
docker compose down
```

## Tools and arguments

Every tool requires `exchange` and `market`. Tools that read one symbol also require its exact `symbol`. Exchange is `binance` or `bybit`; market is `spot` or `linear`. Symbols are case-sensitive and are never changed by this adapter. Input schemas mark required fields and describe optional method settings.

| Tool | Extra required arguments | Source |
| --- | --- | --- |
| `get_instrument` | none | Market Data `ListInstruments` |
| `get_ticker` | none | Market Data `ListTickers` |
| `get_market_stats` | none | Market Data `ListMarketStats`, 24h window |
| `get_candles` | `interval`, `from`, `to` | Market Data `GetKlines` |
| `get_atr`, `get_natr` | `interval`, `to`, `candle_count`, `period` | Analyzer `GetATR`, `GetNATR` |
| `get_extrema` | `interval`, `to`, `candle_count`, `price_source`, `method` and method settings | Analyzer `GetExtrema` |
| `get_trend` | extrema arguments, `equality_tolerance_pct` | Analyzer `GetTrend` |
| `get_levels` | extrema arguments, `zone_atr_period`, `zone_width_atr`, `min_touches`, `min_touch_separation_bars` | Analyzer `GetLevels` |
| `find_active_instruments` | none; `symbol` is not used | Analyzer `FindActiveInstruments` |

Extrema `price_source` is `close` or `high_low`. Method settings are `pivot_span` for `local_extrema`, `reversal_pct` for `reversal_percent`, or both `atr_period` and `atr_multiplier` for `reversal_atr`. Decimal inputs use plain decimal strings. `from` and `to` use RFC3339 timestamps. The candle range is `[from, to)`. MCP accepts `candle_count` from 1 to 1000; Analyzer applies its stricter method requirements. Analyzer selects completed candles using its own alignment rules and reports the actual `source_from` and `source_to` in metadata. See the [Market Data API guide](https://github.com/imbpp123/market-data/blob/main/docs/api.md) and [Analyzer API guide](https://github.com/imbpp123/market-analyzer/blob/main/docs/api.md) for upstream range and setting rules.

For example, a `get_atr` call can use:

```json
{"exchange":"binance","market":"spot","symbol":"BTCUSDT","interval":"1m","to":"2026-09-21T12:00:00Z","candle_count":60,"period":14}
```

Use a recent `to` value inside Market Data's retention window in a live call.

`find_active_instruments` returns trading instruments sorted by symbol. It accepts optional inclusive minimums: `min_volume_24h` (base asset units, decimal string), `min_trades_24h` (integer), and `min_natr` (daily NATR percent, decimal string). All three can be used together. `natr_period` is optional from 1 to 999, defaults to 14, and requires `min_natr`. Omitted thresholds are not applied; a present zero still requires source data. Bybit currently has no trade count, so its instruments cannot pass a trade count threshold. A broad NATR search requests daily candles for each candidate and can exceed the configured timeout. The response includes each match's source values and timestamps when the related filters were applied.

For example:

```json
{"exchange":"binance","market":"spot","min_volume_24h":"1000","min_trades_24h":100,"min_natr":"2","natr_period":14}
```

## Result rules

- Decimal values remain strings. Missing optional fields remain absent; a present zero remains `"0"` or numeric `0` as defined by the upstream field.
- Every successful result has `served_at`. Market Data rows retain `updated_at` or `fetched_at` from the source. Tickers are labeled `cached_ticker`; `fetched_at` is a local receipt time, not a guarantee of a current exchange price.
- Candle results include the requested `source_range` and all returned candles. An oversized candle result fails with `result_too_large`; it is never silently cut.
- Analyzer results preserve `metadata`, effective `settings`, and the typed `result`, including algorithm IDs, numeric policy, `evaluated_at`, `source_from`, and `source_to`. Analyzer's full source candle array is omitted from MCP output on every analysis call. `source_candles.omitted`, `omitted_count`, and `latest_fetched_at` state exactly what was removed. Candle indices in result evidence still refer to that omitted original array. Use `get_candles` for source rows if needed.
- `find_active_instruments` returns the Analyzer `instruments` array and `served_at`. Its optional source values remain absent when not requested. Protobuf `int64` trade counts are JSON strings, including `"0"`.
- Other oversized results fail with `result_too_large`. No incomplete extrema or zone list is presented as complete. An unusually large upstream error is replaced with the bounded `error_response_too_large` error.
- Tool errors are JSON text with `code`, stable `reason` when supplied by upstream, and `message`. Analyzer error details can also include `field`, `upstream_code`, and `upstream_reason`.

## Connect Codex locally

For the Compose HTTP service, this repository already has `.codex/config.toml` pointing to `http://127.0.0.1:8082/mcp`. Start the stack, then open Settings → MCP servers in the ChatGPT desktop app and restart the server. Type `/mcp` in the composer to inspect the connection. The project must be trusted. The Codex CLI is optional. See the [official Codex MCP setup](https://learn.chatgpt.com/docs/extend/mcp?surface=app).

For stdio instead, build the binary and add this entry to `~/.codex/config.toml`. Replace the command path with your binary location:

```toml
[mcp_servers.market_mcp]
command = "/path/to/market-mcp/bin/market-mcp"
tool_timeout_sec = 40
```

## Connect ChatGPT later

ChatGPT web cannot read a local Codex configuration or connect directly to a loopback endpoint on this machine. Two deployment paths are possible:

1. Keep Market MCP on loopback and use [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels) from a host that can reach `http://127.0.0.1:8082/mcp`. Configure the tunnel and ChatGPT developer-mode app in the appropriate organization/workspace. Store tunnel credentials outside this repository.
2. Put an authenticated HTTPS reverse proxy in front of the loopback listener. The proxy must enforce access control and TLS, then forward to `/mcp`. Register that protected remote endpoint in the ChatGPT app. This repository does not provide that proxy or an authentication implementation.

For a local tunnel, create a tunnel in [OpenAI Platform](https://platform.openai.com/settings/organization/tunnels). Pass its ID to `make tunnel-init` and set the runtime API key in your shell. The key is never written by these Make targets:

```sh
make tunnel-init TUNNEL_ID=tunnel_...
read -rs 'CONTROL_PLANE_API_KEY?Runtime API key: '
export CONTROL_PLANE_API_KEY
printf '\n'
make tunnel-run
```

The `read` command above is for zsh. It takes the key without adding it to shell history. `make tunnel-run` runs `tunnel-doctor` first. Keep it running while ChatGPT uses the tools. In another terminal, run `make tunnel-ready` to check the local tunnel client. You can run `make tunnel-doctor` alone to check the profile. `tunnel-init` creates a profile named `market-mcp` and does not replace an existing profile. The tunnel client's admin listener uses `127.0.0.1:8083` to avoid the Market Data, Analyzer, and MCP ports.

Do not expose the unauthenticated HTTP listener directly. See the [official ChatGPT connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt).

## Checks

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

GitHub Actions runs these checks on pushes to `main` and pull requests. Pushing a tag such as `v1.2.3` runs tests, builds a Linux image for amd64 and arm64, and publishes `ghcr.io/imbpp123/market-mcp:v1.2.3` to GitHub Container Registry. The release workflow uses `GITHUB_TOKEN`; no registry secret is needed.
