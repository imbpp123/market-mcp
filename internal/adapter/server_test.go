package adapter

import (
	"encoding/json"
	"testing"

	av1 "github.com/imbpp123/market-analyzer/api/go/marketanalyzer/v1"
	dv1 "github.com/imbpp123/market-data/api/go/marketdata/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolSchemasAndReadOnlyHints(t *testing.T) {
	a := testAdapter(t, &fakeData{}, &fakeAnalyzer{}, DefaultMaxResultBytes)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := a.Server().Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	listed, err := clientSession.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 10)
	requiredByTool := map[string][]string{
		"get_instrument":          {"exchange", "market", "symbol"},
		"get_ticker":              {"exchange", "market", "symbol"},
		"get_market_stats":        {"exchange", "market", "symbol"},
		"get_candles":             {"exchange", "market", "symbol", "interval", "from", "to"},
		"get_atr":                 {"exchange", "market", "symbol", "interval", "to", "candle_count", "period"},
		"get_natr":                {"exchange", "market", "symbol", "interval", "to", "candle_count", "period"},
		"get_extrema":             {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method"},
		"get_trend":               {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method", "equality_tolerance_pct"},
		"get_levels":              {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method", "zone_atr_period", "zone_width_atr", "min_touches", "min_touch_separation_bars"},
		"find_active_instruments": {"exchange", "market"},
	}
	for _, tool := range listed.Tools {
		require.NotNil(t, tool.Annotations)
		assert.True(t, tool.Annotations.ReadOnlyHint, "tool %s", tool.Name)
		b, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var schema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		}
		require.NoError(t, json.Unmarshal(b, &schema))
		for _, field := range requiredByTool[tool.Name] {
			assert.Contains(t, schema.Properties, field, "tool %s", tool.Name)
			assert.Contains(t, schema.Required, field, "tool %s", tool.Name)
		}
		exchange := schema.Properties["exchange"].(map[string]any)
		assert.Len(t, exchange["enum"], 2, "tool %s", tool.Name)
		if tool.Name == "find_active_instruments" {
			for _, field := range []string{"min_volume_24h", "min_trades_24h", "min_natr", "natr_period"} {
				assert.Contains(t, schema.Properties, field)
				assert.NotContains(t, schema.Required, field)
			}
			volume := schema.Properties["min_volume_24h"].(map[string]any)
			natr := schema.Properties["min_natr"].(map[string]any)
			trades := schema.Properties["min_trades_24h"].(map[string]any)
			period := schema.Properties["natr_period"].(map[string]any)
			assert.Equal(t, decimalPattern.String(), volume["pattern"])
			assert.Equal(t, decimalPattern.String(), natr["pattern"])
			assert.Equal(t, float64(0), trades["minimum"])
			assert.Equal(t, float64(1), period["minimum"])
			assert.Equal(t, float64(999), period["maximum"])
		}
	}
}

func TestFindActiveInstrumentsToolCall(t *testing.T) {
	f := &fakeAnalyzer{active: &av1.FindActiveInstrumentsResponse{Instruments: []*av1.ActiveInstrument{{Symbol: "BTCUSDT"}}}}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := a.Server().Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "find_active_instruments",
		Arguments: map[string]any{
			"exchange":       "binance",
			"market":         "spot",
			"min_volume_24h": "1000",
			"min_trades_24h": 0,
			"min_natr":       "2",
			"natr_period":    14,
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	require.NotNil(t, f.activeRequest)
	assert.Equal(t, "1000", f.activeRequest.GetMinVolume_24H())
	assert.NotNil(t, f.activeRequest.MinTrades_24H)
	assert.Equal(t, int64(0), f.activeRequest.GetMinTrades_24H())
	assert.Equal(t, "2", f.activeRequest.GetMinNatr())
	assert.Equal(t, uint32(14), f.activeRequest.GetNatrPeriod())
	rows := payload(t, result)["instruments"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, "BTCUSDT", rows[0].(map[string]any)["symbol"])
}

func TestToolCallUsesExactSymbol(t *testing.T) {
	data := &fakeData{tickers: &dv1.ListTickersResponse{Tickers: []*dv1.Ticker{{Exchange: "binance", Market: "spot", Symbol: "BTCUSDT", LastPrice: "123.45"}}}}
	a := testAdapter(t, data, &fakeAnalyzer{}, DefaultMaxResultBytes)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := a.Server().Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_ticker", Arguments: map[string]any{"exchange": "binance", "market": "spot", "symbol": "BTCUSDT"}})
	require.NoError(t, err)
	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	m := payload(t, result)
	assert.Equal(t, "cached_ticker", m["snapshot_type"])
	assert.Equal(t, "123.45", m["ticker"].(map[string]any)["last_price"])
}
