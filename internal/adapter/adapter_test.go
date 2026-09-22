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
	atr           *av1.GetATRResponse
	levels        *av1.GetLevelsResponse
	active        *av1.FindActiveInstrumentsResponse
	activeRequest *av1.FindActiveInstrumentsRequest
	err           error
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

func (f *fakeAnalyzer) FindActiveInstruments(_ context.Context, req *av1.FindActiveInstrumentsRequest, _ ...grpc.CallOption) (*av1.FindActiveInstrumentsResponse, error) {
	f.activeRequest = req
	return f.active, f.err
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
		{"one second", "2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z", "1s", ""},
		{"eight hours", "2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z", "8h", ""},
		{"three days", "2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z", "3d", ""},
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

func TestAnalyzerIntervalValidation(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		valid    bool
	}{
		{"minute", "1m", true},
		{"month", "1M", true},
		{"one second", "1s", false},
		{"eight hours", "8h", false},
		{"three days", "3d", false},
		{"unknown", "1minute", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection := validSelection()
			selection.Interval = tt.interval

			_, err := selection.request()

			if tt.valid {
				require.NoError(t, err)
				return
			}
			var toolErr *toolError
			require.ErrorAs(t, err, &toolErr)
			assert.Equal(t, "interval", toolErr.Field)
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

func TestActiveRequestPreservesOptionalThresholds(t *testing.T) {
	zeroVolume := "0"
	zeroTrades := int64(0)
	zeroNATR := "0"
	period := uint32(999)
	tests := []struct {
		name   string
		input  activeArgs
		assert func(*testing.T, *av1.FindActiveInstrumentsRequest)
	}{
		{
			name: "omitted thresholds",
			input: activeArgs{
				Exchange: "binance",
				Market:   "spot",
			},
			assert: func(t *testing.T, req *av1.FindActiveInstrumentsRequest) {
				assert.Nil(t, req.MinVolume_24H)
				assert.Nil(t, req.MinTrades_24H)
				assert.Nil(t, req.MinNatr)
				assert.Nil(t, req.NatrPeriod)
			},
		},
		{
			name: "present zeros and maximum period",
			input: activeArgs{
				Exchange:     "bybit",
				Market:       "linear",
				MinVolume24H: &zeroVolume,
				MinTrades24H: &zeroTrades,
				MinNATR:      &zeroNATR,
				NATRPeriod:   &period,
			},
			assert: func(t *testing.T, req *av1.FindActiveInstrumentsRequest) {
				assert.Equal(t, "bybit", req.GetExchange())
				assert.Equal(t, "linear", req.GetMarket())
				assert.Equal(t, "0", req.GetMinVolume_24H())
				assert.NotNil(t, req.MinTrades_24H)
				assert.Equal(t, int64(0), req.GetMinTrades_24H())
				assert.Equal(t, "0", req.GetMinNatr())
				assert.Equal(t, uint32(999), req.GetNatrPeriod())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := tt.input.request()
			require.NoError(t, err)
			tt.assert(t, req)
		})
	}
}

func TestActiveRequestRejectsInvalidInput(t *testing.T) {
	negativeVolume := "-1"
	exponentNATR := "1e2"
	negativeTrades := int64(-1)
	zeroPeriod := uint32(0)
	largePeriod := uint32(1000)
	validNATR := "2"
	tests := []struct {
		name  string
		input activeArgs
		field string
	}{
		{
			name: "exchange",
			input: activeArgs{
				Exchange: "other",
				Market:   "spot",
			},
			field: "exchange",
		},
		{
			name: "market",
			input: activeArgs{
				Exchange: "binance",
				Market:   "other",
			},
			field: "market",
		},
		{
			name: "negative volume",
			input: activeArgs{
				Exchange:     "binance",
				Market:       "spot",
				MinVolume24H: &negativeVolume,
			},
			field: "min_volume_24h",
		},
		{
			name: "negative trades",
			input: activeArgs{
				Exchange:     "binance",
				Market:       "spot",
				MinTrades24H: &negativeTrades,
			},
			field: "min_trades_24h",
		},
		{
			name: "exponent NATR",
			input: activeArgs{
				Exchange: "binance",
				Market:   "spot",
				MinNATR:  &exponentNATR,
			},
			field: "min_natr",
		},
		{
			name: "period without NATR",
			input: activeArgs{
				Exchange:   "binance",
				Market:     "spot",
				NATRPeriod: &largePeriod,
			},
			field: "natr_period",
		},
		{
			name: "zero period",
			input: activeArgs{
				Exchange:   "binance",
				Market:     "spot",
				MinNATR:    &validNATR,
				NATRPeriod: &zeroPeriod,
			},
			field: "natr_period",
		},
		{
			name: "large period",
			input: activeArgs{
				Exchange:   "binance",
				Market:     "spot",
				MinNATR:    &validNATR,
				NATRPeriod: &largePeriod,
			},
			field: "natr_period",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.input.request()
			var e *toolError
			require.ErrorAs(t, err, &e)
			assert.Equal(t, tt.field, e.Field)
		})
	}
}

func TestFindActiveInstrumentsKeepsSourceValues(t *testing.T) {
	volume := "1000.00"
	trades := int64(0)
	natr := "2.125"
	fetched := timestamppb.New(time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC))
	f := &fakeAnalyzer{active: &av1.FindActiveInstrumentsResponse{Instruments: []*av1.ActiveInstrument{{
		Exchange:       "binance",
		Market:         "spot",
		Symbol:         "BTCUSDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Volume_24H:     &volume,
		Trades_24H:     &trades,
		Natr:           &natr,
		StatsFetchedAt: fetched,
		NatrValueTime:  fetched,
	}}}}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	result := a.findActiveInstruments(t.Context(), activeArgs{
		Exchange: "binance",
		Market:   "spot",
	})

	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	m := payload(t, result)
	assert.Equal(t, "2026-09-21T12:00:00Z", m["served_at"])
	rows := m["instruments"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.Equal(t, "BTCUSDT", row["symbol"])
	assert.Equal(t, "1000.00", row["volume_24h"])
	assert.Equal(t, "0", row["trades_24h"])
	assert.Equal(t, "2.125", row["natr"])
	assert.Equal(t, "2026-09-21T11:00:00Z", row["stats_fetched_at"])
	assert.Equal(t, "2026-09-21T11:00:00Z", row["natr_value_time"])
}

func TestFindActiveInstrumentsUpstreamFailure(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "daily candles missing").WithDetails(&av1.ErrorDetail{Reason: "incomplete_data"})
	require.NoError(t, err)
	f := &fakeAnalyzer{err: st.Err()}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	result := a.findActiveInstruments(t.Context(), activeArgs{
		Exchange: "binance",
		Market:   "spot",
	})

	require.True(t, result.IsError)
	m := payload(t, result)
	assert.Equal(t, "failed_precondition", m["code"])
	assert.Equal(t, "incomplete_data", m["reason"])
	assert.NotContains(t, m, "instruments")
}

func TestFindActiveInstrumentsEmptyResult(t *testing.T) {
	f := &fakeAnalyzer{active: &av1.FindActiveInstrumentsResponse{}}
	a := testAdapter(t, &fakeData{}, f, DefaultMaxResultBytes)
	result := a.findActiveInstruments(t.Context(), activeArgs{
		Exchange: "binance",
		Market:   "spot",
	})

	require.False(t, result.IsError, "tool error = %v", payload(t, result))
	assert.Equal(t, []any{}, payload(t, result)["instruments"])
}

func TestFindActiveInstrumentsOversizeFailsWithoutPartialData(t *testing.T) {
	f := &fakeAnalyzer{active: &av1.FindActiveInstrumentsResponse{}}
	for range 100 {
		f.active.Instruments = append(f.active.Instruments, &av1.ActiveInstrument{Symbol: strings.Repeat("X", 128)})
	}
	a := testAdapter(t, &fakeData{}, f, 1024)
	result := a.findActiveInstruments(t.Context(), activeArgs{
		Exchange: "binance",
		Market:   "spot",
	})

	require.True(t, result.IsError)
	m := payload(t, result)
	assert.Equal(t, "result_too_large", m["reason"])
	assert.NotContains(t, m, "instruments")
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
