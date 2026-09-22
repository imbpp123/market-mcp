package adapter

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func inputSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	schema.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	for name, values := range map[string][]any{
		"exchange":     {"binance", "bybit"},
		"market":       {"spot", "linear"},
		"interval":     {"1s", "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w", "1M"},
		"price_source": {"close", "high_low"},
		"method":       {"local_extrema", "reversal_percent", "reversal_atr"},
	} {
		if field := schema.Properties[name]; field != nil {
			field.Enum = values
		}
	}
	for name, minimum := range map[string]float64{
		"candle_count":              1,
		"period":                    1,
		"pivot_span":                1,
		"atr_period":                1,
		"zone_atr_period":           1,
		"min_touches":               2,
		"min_touch_separation_bars": 1,
		"min_trades_24h":            0,
		"natr_period":               1,
	} {
		if field := schema.Properties[name]; field != nil {
			field.Minimum = &minimum
		}
	}
	if field := schema.Properties["candle_count"]; field != nil {
		maximum := float64(1000)
		field.Maximum = &maximum
	}
	if field := schema.Properties["natr_period"]; field != nil {
		maximum := float64(999)
		field.Maximum = &maximum
	}
	for _, name := range []string{"from", "to"} {
		if field := schema.Properties[name]; field != nil {
			field.Format = "date-time"
		}
	}
	for _, name := range []string{"reversal_pct", "atr_multiplier", "equality_tolerance_pct", "zone_width_atr", "min_volume_24h", "min_natr"} {
		if field := schema.Properties[name]; field != nil {
			field.Pattern = decimalPattern.String()
		}
	}
	return schema
}

func analyzerInputSchema[T any]() *jsonschema.Schema {
	schema := inputSchema[T]()
	schema.Properties["interval"].Enum = []any{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d", "1w", "1M"}
	return schema
}

func (a *Adapter) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "market-mcp", Version: "0.1.0"}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "get_instrument", Description: "Read one cached instrument catalog entry by exact exchange, market, and symbol. Use to inspect metadata or verify an instrument exists; this does not analyze prices.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "instrument", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_ticker", Description: "Read one cached ticker by exact exchange, market, and symbol. Use for the latest available cached price or to compare price with Analyzer levels. fetched_at is local receipt time, not a guarantee of a live exchange price.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "ticker", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_market_stats", Description: "Read cached rolling 24-hour statistics for one exact exchange, market, and symbol. Use for recent base-asset volume and available trade count; Bybit has no trade count. Snapshot times can differ from ticker and Analyzer data.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "market_stats", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_candles", Description: "Read raw candles in the complete half-open range [from, to) for candle-level price action. Use Analyzer tools for ATR, NATR, extrema, structural trend, or support and resistance zones. The range must be valid and available; oversized results fail without truncation.", Annotations: readOnly, InputSchema: inputSchema[candleArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in candleArgs) (*mcp.CallToolResult, any, error) {
			return a.candles(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_atr", Description: "Calculate the latest Wilder ATR to measure absolute price volatility for one instrument and timeframe. Uses candle_count closed candles aligned to the boundary at or before to. Returns calculation metadata and evidence, not a trading signal.", Annotations: readOnly, InputSchema: analyzerInputSchema[atrArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in atrArgs) (*mcp.CallToolResult, any, error) {
			return a.atr(ctx, in, false), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_natr", Description: "Calculate the latest NATR percentage, ATR, and reference close from candle_count closed candles aligned using to. Use to compare normalized volatility across instruments or timeframes; this is analysis data, not a trading signal.", Annotations: readOnly, InputSchema: analyzerInputSchema[atrArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in atrArgs) (*mcp.CallToolResult, any, error) {
			return a.atr(ctx, in, true), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_extrema", Description: "Find confirmed price extrema from candle_count closed candles aligned using to. Use for swing highs, lows, and structural turning points. Choose price_source and one method with matching settings; unfinished reversal candidates are excluded.", Annotations: readOnly, InputSchema: analyzerInputSchema[extremaArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in extremaArgs) (*mcp.CallToolResult, any, error) {
			return a.extrema(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_trend", Description: "Analyze structural trend from confirmed extrema for one instrument and timeframe, using candle_count closed candles aligned by to. Returns trend state, reason, extrema, and evidence for market-structure or multi-timeframe analysis, not a trading signal.", Annotations: readOnly, InputSchema: analyzerInputSchema[trendArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in trendArgs) (*mcp.CallToolResult, any, error) {
			return a.trend(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_levels", Description: "Find support and resistance zones from confirmed extrema and ATR-based zone settings for one instrument and timeframe, using closed candles aligned by to and candle_count. Returns zones and evidence; use get_ticker for cached price context and get_trend for structure. No entry or exit signals.", Annotations: readOnly, InputSchema: analyzerInputSchema[levelsArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in levelsArgs) (*mcp.CallToolResult, any, error) {
			return a.levels(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_active_instruments",
		Description: "Primary market scanning tool: find currently trading instruments meeting optional minimum rolling 24-hour base-asset volume, trade count, and daily NATR percentage. Results are sorted by symbol; analyze candidates with get_ticker, get_market_stats, get_trend, get_levels, get_extrema, or get_candles. NATR uses closed daily candles (default period 14); narrow with 24-hour thresholds because broad NATR scans may time out. Missing required data excludes candidates or fails the request; this is a filter, not a trading signal.",
		Annotations: readOnly,
		InputSchema: inputSchema[activeArgs](),
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in activeArgs) (*mcp.CallToolResult, any, error) {
			return a.findActiveInstruments(ctx, in), nil, nil
		})
	return s
}
