package adapter

import (
	"encoding/json"
	"testing"

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
	require.Len(t, listed.Tools, 9)
	requiredByTool := map[string][]string{
		"get_instrument":   {"exchange", "market", "symbol"},
		"get_ticker":       {"exchange", "market", "symbol"},
		"get_market_stats": {"exchange", "market", "symbol"},
		"get_candles":      {"exchange", "market", "symbol", "interval", "from", "to"},
		"get_atr":          {"exchange", "market", "symbol", "interval", "to", "candle_count", "period"},
		"get_natr":         {"exchange", "market", "symbol", "interval", "to", "candle_count", "period"},
		"get_extrema":      {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method"},
		"get_trend":        {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method", "equality_tolerance_pct"},
		"get_levels":       {"exchange", "market", "symbol", "interval", "to", "candle_count", "price_source", "method", "zone_atr_period", "zone_width_atr", "min_touches", "min_touch_separation_bars"},
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
	}
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
