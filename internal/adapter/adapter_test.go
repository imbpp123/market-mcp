package adapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	av1 "github.com/imbpp123/market-analyzer/api/go/marketanalyzer/v1"
	dv1 "github.com/imbpp123/market-data/api/go/marketdata/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeData struct {
	instruments *dv1.ListInstrumentsResponse
	tickers     *dv1.ListTickersResponse
	stats       *dv1.ListMarketStatsResponse
	klines      *dv1.GetKlinesResponse
	err         error
	deadline    time.Time
}

func (f *fakeData) ListInstruments(context.Context, *dv1.ListInstrumentsRequest, ...grpc.CallOption) (*dv1.ListInstrumentsResponse, error) {
	return f.instruments, f.err
}

func (f *fakeData) ListTickers(ctx context.Context, _ *dv1.ListTickersRequest, _ ...grpc.CallOption) (*dv1.ListTickersResponse, error) {
	f.deadline, _ = ctx.Deadline()
	return f.tickers, f.err
}

func (f *fakeData) ListMarketStats(context.Context, *dv1.ListMarketStatsRequest, ...grpc.CallOption) (*dv1.ListMarketStatsResponse, error) {
	return f.stats, f.err
}

func (f *fakeData) GetKlines(context.Context, *dv1.GetKlinesRequest, ...grpc.CallOption) (*dv1.GetKlinesResponse, error) {
	return f.klines, f.err
}

type fakeAnalyzer struct {
	atr    *av1.GetATRResponse
	levels *av1.GetLevelsResponse
	err    error
}

func (f *fakeAnalyzer) GetATR(context.Context, *av1.GetATRRequest, ...grpc.CallOption) (*av1.GetATRResponse, error) {
	return f.atr, f.err
}

func (f *fakeAnalyzer) GetNATR(context.Context, *av1.GetNATRRequest, ...grpc.CallOption) (*av1.GetNATRResponse, error) {
	return nil, f.err
}

func (f *fakeAnalyzer) GetExtrema(context.Context, *av1.GetExtremaRequest, ...grpc.CallOption) (*av1.GetExtremaResponse, error) {
	return nil, f.err
}

func (f *fakeAnalyzer) GetTrend(context.Context, *av1.GetTrendRequest, ...grpc.CallOption) (*av1.GetTrendResponse, error) {
	return nil, f.err
}

func (f *fakeAnalyzer) GetLevels(context.Context, *av1.GetLevelsRequest, ...grpc.CallOption) (*av1.GetLevelsResponse, error) {
	return f.levels, f.err
}

func testAdapter(t *testing.T, data *fakeData, analyzer *fakeAnalyzer, maxBytes int) *Adapter {
	t.Helper()
	a, err := New(data, analyzer, time.Second, maxBytes)
	require.NoError(t, err)
	a.now = func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) }
	return a
}

func payload(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	require.Len(t, result.Content, 1)
	content, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok, "content type = %T", result.Content[0])
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(content.Text), &value))
	return value
}

func validIdentity() identity {
	return identity{Exchange: "binance", Market: "spot", Symbol: "BTCUSDT"}
}

func validSelection() selectionArgs {
	return selectionArgs{identity: validIdentity(), Interval: "1m", To: "2026-09-21T11:00:00Z", CandleCount: 30}
}

func TestIdentityValidation(t *testing.T) {
	tests := []struct {
		name  string
		input identity
		field string
	}{
		{"valid", validIdentity(), ""},
		{"exchange", identity{Exchange: "BINANCE", Market: "spot", Symbol: "BTCUSDT"}, "exchange"},
		{"market", identity{Exchange: "binance", Market: "other", Symbol: "BTCUSDT"}, "market"},
		{"symbol empty", identity{Exchange: "binance", Market: "spot"}, "symbol"},
		{"symbol space", identity{Exchange: "binance", Market: "spot", Symbol: "BTC USDT"}, "symbol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.validate()
			if tt.field == "" {
				require.NoError(t, err)
				return
			}
			var e *toolError
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tt.field, e.Field)
		})
	}
}

func TestCandleRangeValidation(t *testing.T) {
	tests := []struct{ name, from, to, interval, field string }{
		{"valid", "2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z", "1m", ""},
		{"bad from", "yesterday", "2026-09-21T10:01:00Z", "1m", "from"},
		{"reversed", "2026-09-21T10:02:00Z", "2026-09-21T10:01:00Z", "1m", "from"},
		{"bad interval", "2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z", "1minute", "interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (candleArgs{identity: validIdentity(), From: tt.from, To: tt.to, Interval: tt.interval}).request()
			if tt.field == "" {
				require.NoError(t, err)
				return
			}
			var e *toolError
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tt.field, e.Field)
		})
	}
}

func TestExtremaSettings(t *testing.T) {
	tests := []struct {
		name  string
		input extremaArgs
		valid bool
	}{
		{"local", extremaArgs{Method: "local_extrema", PriceSource: "close", PivotSpan: 2}, true},
		{"missing span", extremaArgs{Method: "local_extrema", PriceSource: "close"}, false},
		{"extra setting", extremaArgs{Method: "local_extrema", PriceSource: "close", PivotSpan: 1, ReversalPct: "2"}, false},
		{"percent", extremaArgs{Method: "reversal_percent", PriceSource: "high_low", ReversalPct: "2.5"}, true},
		{"exponent", extremaArgs{Method: "reversal_percent", PriceSource: "close", ReversalPct: "1e2"}, false},
		{"zero", extremaArgs{Method: "reversal_percent", PriceSource: "close", ReversalPct: "0"}, false},
		{"atr", extremaArgs{Method: "reversal_atr", PriceSource: "close", ATRPeriod: 14, ATRMultiplier: "1.5"}, true},
		{"missing multiplier", extremaArgs{Method: "reversal_atr", PriceSource: "close", ATRPeriod: 14}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input.settings()
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestOptionalDecimalAndZero(t *testing.T) {
	fetched := timestamppb.New(time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC))
	zero := "0"
	msg := &dv1.ListTickersResponse{Tickers: []*dv1.Ticker{{Exchange: "binance", Market: "spot", Symbol: "BTCUSDT", LastPrice: "100.000", BidPrice: &zero, FetchedAt: fetched}}}
	m, err := protoMap(msg)
	require.NoError(t, err)
	rows := m["tickers"].([]any)
	ticker := rows[0].(map[string]any)
	assert.Equal(t, "100.000", ticker["last_price"])
	assert.Equal(t, "0", ticker["bid_price"])
	assert.NotContains(t, ticker, "ask_price")
}

func TestSnapshotNotFoundAndUpstreamReason(t *testing.T) {
	detail, err := status.New(codes.Unavailable, "snapshot not ready").WithDetails(&dv1.ErrorDetail{Reason: "data_not_ready"})
	require.NoError(t, err)
	tests := []struct {
		name   string
		data   *fakeData
		reason string
	}{
		{"empty", &fakeData{tickers: &dv1.ListTickersResponse{}}, "symbol_not_found"},
		{"upstream", &fakeData{err: detail.Err()}, "data_not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := testAdapter(t, tt.data, &fakeAnalyzer{}, DefaultMaxResultBytes)
			result := a.snapshot(t.Context(), "ticker", validIdentity())
			require.True(t, result.IsError)
			assert.Equal(t, tt.reason, payload(t, result)["reason"])
		})
	}
}

func TestSnapshotSetsUpstreamDeadline(t *testing.T) {
	data := &fakeData{tickers: &dv1.ListTickersResponse{}}
	a := testAdapter(t, data, &fakeAnalyzer{}, DefaultMaxResultBytes)
	a.snapshot(t.Context(), "ticker", validIdentity())
	assert.False(t, data.deadline.IsZero())
}

func TestAnalyzerReasonFields(t *testing.T) {
	upCode, upReason := "Unavailable", "data_not_ready"
	st, err := status.New(codes.Unavailable, "source unavailable").WithDetails(&av1.ErrorDetail{Reason: "market_data_unavailable", UpstreamCode: &upCode, UpstreamReason: &upReason})
	require.NoError(t, err)
	e := upstream(st.Err())
	assert.Equal(t, "market_data_unavailable", e.Reason)
	assert.Equal(t, "data_not_ready", e.UpstreamReason)
	assert.Equal(t, "Unavailable", e.UpstreamCode)
}

func TestAnalyzerOmitsCandlesAndKeepsMetadata(t *testing.T) {
	fetched := timestamppb.New(time.Date(2026, 9, 21, 10, 1, 0, 0, time.UTC))
	from := timestamppb.New(time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC))
	to := timestamppb.New(time.Date(2026, 9, 21, 10, 1, 0, 0, time.UTC))
	f := &fakeAnalyzer{atr: &av1.GetATRResponse{
		Metadata: &av1.Metadata{EvaluatedAt: to, SourceFrom: from, SourceTo: to, AlgorithmIds: []string{"wilder_atr_v1"}, NumericPolicy: "decimal_sig16_output8_pct6_v1"},
		Candles:  []*av1.Candle{{Open: "100", Close: "101", FetchedAt: fetched}},
		Settings: &av1.ATRSettings{Period: uint32Ptr(14)},
		Result:   &av1.ATRResult{Value: "1.25", CandleIndex: 29, ValueTime: to},
	}}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	result := a.atr(t.Context(), atrArgs{selectionArgs: validSelection(), Period: 14}, false)
	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	m := payload(t, result)
	assert.NotContains(t, m, "candles")
	source := m["source_candles"].(map[string]any)
	assert.Equal(t, true, source["omitted"])
	assert.Equal(t, float64(1), source["omitted_count"])
	assert.Equal(t, "2026-09-21T10:01:00Z", source["latest_fetched_at"])
	metadata := m["metadata"].(map[string]any)
	assert.Equal(t, "2026-09-21T10:00:00Z", metadata["source_from"])
	assert.Equal(t, "2026-09-21T10:01:00Z", metadata["source_to"])
	assert.Equal(t, "1.25", m["result"].(map[string]any)["value"])
}

func TestLevelsKeepEvidenceIndicesAndOptionalZero(t *testing.T) {
	zero := int64(0)
	f := &fakeAnalyzer{levels: &av1.GetLevelsResponse{
		Metadata: &av1.Metadata{AlgorithmIds: []string{"local_extrema_v1", "wilder_atr_v1", "pivot_zones_v1"}},
		Candles:  []*av1.Candle{{TradesCount: &zero}},
		Result: &av1.LevelsResult{
			Zones:   []*av1.PriceZone{{LowerBound: "100.00", UpperBound: "100.00", ExtremumIndices: []uint32{0}, AcceptedCandleIndices: []uint32{0}, TouchCount: 2}},
			Extrema: &av1.ExtremaResult{Points: []*av1.Extremum{{CandleIndex: 0, Price: "100.00"}}},
			Atr:     &av1.ATRResult{Value: "0"},
		},
	}}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	result := a.levels(t.Context(), levelsArgs{
		extremaArgs:   extremaArgs{selectionArgs: validSelection(), PriceSource: "close", Method: "local_extrema", PivotSpan: 1},
		ZoneATRPeriod: 14, ZoneWidthATR: "1", MinTouches: 2, MinTouchSeparationBars: 1,
	})
	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	m := payload(t, result)
	assert.Equal(t, float64(1), m["source_candles"].(map[string]any)["omitted_count"])
	zones := m["result"].(map[string]any)["zones"].([]any)
	zone := zones[0].(map[string]any)
	assert.Equal(t, "100.00", zone["lower_bound"])
	assert.Equal(t, float64(0), zone["accepted_candle_indices"].([]any)[0])
	assert.Equal(t, "0", m["result"].(map[string]any)["atr"].(map[string]any)["value"])
}

func TestNativeUpstreamStatusUsesFallbackReason(t *testing.T) {
	e := upstream(status.Error(codes.DeadlineExceeded, "deadline exceeded"))
	assert.Equal(t, "deadline_exceeded", e.Code)
	assert.Equal(t, "request_timeout", e.Reason)
}

func TestCandlesOversizeFailsWithoutPartialData(t *testing.T) {
	data := &fakeData{klines: &dv1.GetKlinesResponse{Exchange: "binance", Market: "spot", Symbol: "BTCUSDT", Interval: "1m"}}
	for range 100 {
		data.klines.Klines = append(data.klines.Klines, &dv1.Kline{Open: strings.Repeat("1", 128), High: "2", Low: "1", Close: "2", Volume: "1", Turnover: "2"})
	}
	a := testAdapter(t, data, &fakeAnalyzer{}, 1024)
	result := a.candles(t.Context(), candleArgs{identity: validIdentity(), Interval: "1m", From: "2026-09-21T10:00:00Z", To: "2026-09-21T10:01:00Z"})
	require.True(t, result.IsError)
	m := payload(t, result)
	assert.Equal(t, "result_too_large", m["reason"])
	assert.NotContains(t, m, "klines")
}

func TestResultLimitIncludesMCPEnvelope(t *testing.T) {
	value := map[string]any{"payload": strings.Repeat("x", 1000)}
	inner, err := json.Marshal(map[string]any{"payload": value["payload"], "served_at": "2026-09-21T12:00:00Z"})
	require.NoError(t, err)
	limit := len(inner) + 1
	require.GreaterOrEqual(t, limit, 1024)
	a := testAdapter(t, &fakeData{}, &fakeAnalyzer{}, limit)

	result := a.result(value, nil)
	require.True(t, result.IsError)
	assert.Equal(t, "result_too_large", payload(t, result)["reason"])
}

func TestErrorResultLimit(t *testing.T) {
	a := testAdapter(t, &fakeData{}, &fakeAnalyzer{}, 1024)
	result := a.result(nil, &toolError{Code: "unavailable", Reason: strings.Repeat("x", 5000), Message: "upstream error"})

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), 1024)
	assert.Equal(t, "error_response_too_large", payload(t, result)["reason"])
}

func uint32Ptr(v uint32) *uint32 { return &v }
