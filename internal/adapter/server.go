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
	} {
		if field := schema.Properties[name]; field != nil {
			field.Minimum = &minimum
		}
	}
	if field := schema.Properties["candle_count"]; field != nil {
		maximum := float64(1000)
		field.Maximum = &maximum
	}
	for _, name := range []string{"from", "to"} {
		if field := schema.Properties[name]; field != nil {
			field.Format = "date-time"
		}
	}
	for _, name := range []string{"reversal_pct", "atr_multiplier", "equality_tolerance_pct", "zone_width_atr"} {
		if field := schema.Properties[name]; field != nil {
			field.Pattern = decimalPattern.String()
		}
	}
	return schema
}

func (a *Adapter) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "market-mcp", Version: "0.1.0"}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

	mcp.AddTool(s, &mcp.Tool{Name: "get_instrument", Description: "Read one cached instrument catalog entry by exact exchange, market, and symbol.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "instrument", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_ticker", Description: "Read one cached ticker. fetched_at is local receipt time; this is not a guaranteed live exchange price.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "ticker", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_market_stats", Description: "Read cached rolling 24-hour market statistics for one exact symbol.", Annotations: readOnly, InputSchema: inputSchema[identity]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in identity) (*mcp.CallToolResult, any, error) {
			return a.snapshot(ctx, "market_stats", in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_candles", Description: "Read a complete half-open candle range [from, to). Oversized results fail without truncation.", Annotations: readOnly, InputSchema: inputSchema[candleArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in candleArgs) (*mcp.CallToolResult, any, error) {
			return a.candles(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_atr", Description: "Read Analyzer Wilder ATR for a closed candle selection. Source candles are omitted and counted.", Annotations: readOnly, InputSchema: inputSchema[atrArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in atrArgs) (*mcp.CallToolResult, any, error) {
			return a.atr(ctx, in, false), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_natr", Description: "Read Analyzer NATR percentage for a closed candle selection. Source candles are omitted and counted.", Annotations: readOnly, InputSchema: inputSchema[atrArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in atrArgs) (*mcp.CallToolResult, any, error) {
			return a.atr(ctx, in, true), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_extrema", Description: "Read confirmed Analyzer extrema. Set method and its matching settings; source candles are omitted and counted.", Annotations: readOnly, InputSchema: inputSchema[extremaArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in extremaArgs) (*mcp.CallToolResult, any, error) {
			return a.extrema(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_trend", Description: "Read Analyzer trend state and evidence; no trading signal. Source candles are omitted and counted.", Annotations: readOnly, InputSchema: inputSchema[trendArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in trendArgs) (*mcp.CallToolResult, any, error) {
			return a.trend(ctx, in), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_levels", Description: "Read Analyzer support and resistance zones and evidence; no trading signal. Source candles are omitted and counted.", Annotations: readOnly, InputSchema: inputSchema[levelsArgs]()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in levelsArgs) (*mcp.CallToolResult, any, error) {
			return a.levels(ctx, in), nil, nil
		})
	return s
}
