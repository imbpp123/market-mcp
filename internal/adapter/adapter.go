package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	av1 "github.com/imbpp123/market-analyzer/api/go/marketanalyzer/v1"
	dv1 "github.com/imbpp123/market-data/api/go/marketdata/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const DefaultMaxResultBytes = 256 << 10

type dataClient interface {
	ListInstruments(context.Context, *dv1.ListInstrumentsRequest, ...grpc.CallOption) (*dv1.ListInstrumentsResponse, error)
	ListTickers(context.Context, *dv1.ListTickersRequest, ...grpc.CallOption) (*dv1.ListTickersResponse, error)
	ListMarketStats(context.Context, *dv1.ListMarketStatsRequest, ...grpc.CallOption) (*dv1.ListMarketStatsResponse, error)
	GetKlines(context.Context, *dv1.GetKlinesRequest, ...grpc.CallOption) (*dv1.GetKlinesResponse, error)
}

type analyzerClient interface {
	GetATR(context.Context, *av1.GetATRRequest, ...grpc.CallOption) (*av1.GetATRResponse, error)
	GetNATR(context.Context, *av1.GetNATRRequest, ...grpc.CallOption) (*av1.GetNATRResponse, error)
	GetExtrema(context.Context, *av1.GetExtremaRequest, ...grpc.CallOption) (*av1.GetExtremaResponse, error)
	GetTrend(context.Context, *av1.GetTrendRequest, ...grpc.CallOption) (*av1.GetTrendResponse, error)
	GetLevels(context.Context, *av1.GetLevelsRequest, ...grpc.CallOption) (*av1.GetLevelsResponse, error)
	FindActiveInstruments(context.Context, *av1.FindActiveInstrumentsRequest, ...grpc.CallOption) (*av1.FindActiveInstrumentsResponse, error)
}

type Adapter struct {
	data     dataClient
	analyzer analyzerClient
	timeout  time.Duration
	maxBytes int
	now      func() time.Time
}

func New(data dataClient, analyzer analyzerClient, timeout time.Duration, maxBytes int) (*Adapter, error) {
	if data == nil || analyzer == nil || timeout <= 0 || maxBytes < 1024 {
		return nil, errors.New("invalid adapter settings")
	}
	return &Adapter{data: data, analyzer: analyzer, timeout: timeout, maxBytes: maxBytes, now: time.Now}, nil
}

type identity struct {
	Exchange string `json:"exchange" jsonschema:"Exchange: binance or bybit"`
	Market   string `json:"market" jsonschema:"Market: spot or linear"`
	Symbol   string `json:"symbol" jsonschema:"Exact exchange symbol; case is preserved"`
}

type candleArgs struct {
	identity
	Interval string `json:"interval" jsonschema:"Candle interval, such as 1m or 1h"`
	From     string `json:"from" jsonschema:"Inclusive UTC time in RFC3339 format, aligned to interval"`
	To       string `json:"to" jsonschema:"Exclusive UTC time in RFC3339 format, aligned to interval"`
}

type selectionArgs struct {
	identity
	Interval    string `json:"interval" jsonschema:"Candle interval, such as 1m or 1h"`
	To          string `json:"to" jsonschema:"Analysis time in RFC3339 format"`
	CandleCount uint32 `json:"candle_count" jsonschema:"Number of closed source candles"`
}

type atrArgs struct {
	selectionArgs
	Period uint32 `json:"period" jsonschema:"Positive ATR period; candle_count must be at least period plus one"`
}

type extremaArgs struct {
	selectionArgs
	PriceSource   string `json:"price_source" jsonschema:"close or high_low"`
	Method        string `json:"method" jsonschema:"local_extrema, reversal_percent, or reversal_atr"`
	PivotSpan     uint32 `json:"pivot_span,omitempty" jsonschema:"Required for local_extrema"`
	ReversalPct   string `json:"reversal_pct,omitempty" jsonschema:"Required for reversal_percent; decimal percent between 0 and 100"`
	ATRPeriod     uint32 `json:"atr_period,omitempty" jsonschema:"Required for reversal_atr"`
	ATRMultiplier string `json:"atr_multiplier,omitempty" jsonschema:"Required for reversal_atr; positive decimal"`
}

type trendArgs struct {
	extremaArgs
	EqualityTolerancePct string `json:"equality_tolerance_pct" jsonschema:"Nonnegative decimal percentage"`
}

type levelsArgs struct {
	extremaArgs
	ZoneATRPeriod          uint32 `json:"zone_atr_period" jsonschema:"Positive ATR period for zone width"`
	ZoneWidthATR           string `json:"zone_width_atr" jsonschema:"Positive decimal ATR multiplier for zone width"`
	MinTouches             uint32 `json:"min_touches" jsonschema:"At least two independent touches"`
	MinTouchSeparationBars uint32 `json:"min_touch_separation_bars" jsonschema:"Positive number of bars between touches"`
}

type activeArgs struct {
	Exchange     string  `json:"exchange" jsonschema:"Exchange: binance or bybit"`
	Market       string  `json:"market" jsonschema:"Market: spot or linear"`
	MinVolume24H *string `json:"min_volume_24h,omitempty" jsonschema:"Optional minimum 24-hour volume in base asset units; plain nonnegative decimal string"`
	MinTrades24H *int64  `json:"min_trades_24h,omitempty" jsonschema:"Optional minimum 24-hour trade count; Bybit has no trade count"`
	MinNATR      *string `json:"min_natr,omitempty" jsonschema:"Optional minimum daily NATR percentage; plain nonnegative decimal string"`
	NATRPeriod   *uint32 `json:"natr_period,omitempty" jsonschema:"Optional NATR period from 1 to 999; requires min_natr; default 14"`
}

type toolError struct {
	Code           string `json:"code"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	Field          string `json:"field,omitempty"`
	UpstreamCode   string `json:"upstream_code,omitempty"`
	UpstreamReason string `json:"upstream_reason,omitempty"`
}

func (e *toolError) Error() string { return e.Message }

func invalid(field, message string) error {
	return &toolError{Code: "invalid_argument", Reason: "invalid_parameter", Field: field, Message: message}
}

var decimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)

func decimal(field, value string, allowZero bool) error {
	if len(value) == 0 || len(value) > 1024 || !decimalPattern.MatchString(value) {
		return invalid(field, "invalid decimal value")
	}
	// The analyzer checks exact bounds. This local check only rejects zero and negatives.
	positive := false
	for _, ch := range strings.TrimPrefix(value, "+") {
		if ch >= '1' && ch <= '9' {
			positive = true
			break
		}
	}
	if strings.HasPrefix(value, "-") || (!allowZero && !positive) {
		return invalid(field, "decimal value is outside the allowed range")
	}
	return nil
}

func (a identity) validate() error {
	if a.Exchange != "binance" && a.Exchange != "bybit" {
		return invalid("exchange", "exchange must be binance or bybit")
	}
	if a.Market != "spot" && a.Market != "linear" {
		return invalid("market", "market must be spot or linear")
	}
	if len(a.Symbol) < 1 || len(a.Symbol) > 128 || strings.IndexFunc(a.Symbol, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return invalid("symbol", "symbol must be an exact nonempty exchange symbol")
	}
	return nil
}

func validInterval(s string) bool {
	switch s {
	case "1s", "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w", "1M":
		return true
	}
	return false
}

func validAnalyzerInterval(s string) bool {
	switch s {
	case "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d", "1w", "1M":
		return true
	}
	return false
}

func parseTime(field, s string) (*timestamppb.Timestamp, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Before(time.Unix(0, 0)) {
		return nil, invalid(field, "time must be RFC3339 and at or after the Unix epoch")
	}
	return timestamppb.New(t), nil
}

func (a candleArgs) request() (*dv1.GetKlinesRequest, error) {
	if err := a.identity.validate(); err != nil {
		return nil, err
	}
	if !validInterval(a.Interval) {
		return nil, invalid("interval", "unsupported interval")
	}
	from, err := parseTime("from", a.From)
	if err != nil {
		return nil, err
	}
	to, err := parseTime("to", a.To)
	if err != nil {
		return nil, err
	}
	if from.AsTime().After(to.AsTime()) {
		return nil, invalid("from", "from must not be after to")
	}
	return &dv1.GetKlinesRequest{Exchange: &a.Exchange, Market: &a.Market, Symbol: &a.Symbol, Interval: &a.Interval, From: from, To: to}, nil
}

func (a selectionArgs) request() (*av1.Selection, error) {
	if err := a.identity.validate(); err != nil {
		return nil, err
	}
	if !validAnalyzerInterval(a.Interval) {
		return nil, invalid("interval", "unsupported interval")
	}
	to, err := parseTime("to", a.To)
	if err != nil {
		return nil, err
	}
	if a.CandleCount == 0 || a.CandleCount > 1000 {
		return nil, invalid("candle_count", "candle_count must be between 1 and 1000")
	}
	return &av1.Selection{Exchange: &a.Exchange, Market: &a.Market, Symbol: &a.Symbol, To: to, CandleCount: &a.CandleCount, Interval: &a.Interval}, nil
}

func (a activeArgs) request() (*av1.FindActiveInstrumentsRequest, error) {
	if a.Exchange != "binance" && a.Exchange != "bybit" {
		return nil, invalid("exchange", "exchange must be binance or bybit")
	}
	if a.Market != "spot" && a.Market != "linear" {
		return nil, invalid("market", "market must be spot or linear")
	}
	if a.MinVolume24H != nil {
		if err := decimal("min_volume_24h", *a.MinVolume24H, true); err != nil {
			return nil, err
		}
	}
	if a.MinTrades24H != nil && *a.MinTrades24H < 0 {
		return nil, invalid("min_trades_24h", "min_trades_24h must be nonnegative")
	}
	if a.MinNATR != nil {
		if err := decimal("min_natr", *a.MinNATR, true); err != nil {
			return nil, err
		}
	}
	if a.NATRPeriod != nil && (a.MinNATR == nil || *a.NATRPeriod == 0 || *a.NATRPeriod > 999) {
		return nil, invalid("natr_period", "natr_period requires min_natr and must be between 1 and 999")
	}

	return &av1.FindActiveInstrumentsRequest{
		Exchange:      &a.Exchange,
		Market:        &a.Market,
		MinVolume_24H: a.MinVolume24H,
		MinTrades_24H: a.MinTrades24H,
		MinNatr:       a.MinNATR,
		NatrPeriod:    a.NATRPeriod,
	}, nil
}

func (a extremaArgs) settings() (*av1.ExtremaSettings, error) {
	var source av1.PriceSource
	switch a.PriceSource {
	case "close":
		source = av1.PriceSource_PRICE_SOURCE_CLOSE
	case "high_low":
		source = av1.PriceSource_PRICE_SOURCE_HIGH_LOW
	default:
		return nil, invalid("price_source", "price_source must be close or high_low")
	}
	s := &av1.ExtremaSettings{PriceSource: &source}
	switch a.Method {
	case "local_extrema":
		if a.PivotSpan == 0 || a.ReversalPct != "" || a.ATRPeriod != 0 || a.ATRMultiplier != "" {
			return nil, invalid("method", "local_extrema requires pivot_span only")
		}
		s.Method = &av1.ExtremaSettings_LocalExtrema{LocalExtrema: &av1.LocalExtremaSettings{PivotSpan: &a.PivotSpan}}
	case "reversal_percent":
		if a.PivotSpan != 0 || a.ATRPeriod != 0 || a.ATRMultiplier != "" {
			return nil, invalid("method", "reversal_percent requires reversal_pct only")
		}
		if err := decimal("reversal_pct", a.ReversalPct, false); err != nil {
			return nil, err
		}
		s.Method = &av1.ExtremaSettings_ReversalPercent{ReversalPercent: &av1.PercentReversalSettings{ReversalPct: &a.ReversalPct}}
	case "reversal_atr":
		if a.PivotSpan != 0 || a.ReversalPct != "" || a.ATRPeriod == 0 {
			return nil, invalid("method", "reversal_atr requires atr_period and atr_multiplier only")
		}
		if err := decimal("atr_multiplier", a.ATRMultiplier, false); err != nil {
			return nil, err
		}
		s.Method = &av1.ExtremaSettings_ReversalAtr{ReversalAtr: &av1.ATRReversalSettings{AtrPeriod: &a.ATRPeriod, AtrMultiplier: &a.ATRMultiplier}}
	default:
		return nil, invalid("method", "unsupported extrema method")
	}
	return s, nil
}

func (a *Adapter) context(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, a.timeout)
}

func protoMap(msg proto.Message) (map[string]any, error) {
	b, err := (protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func upstream(err error) *toolError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &toolError{Code: "deadline_exceeded", Reason: "request_timeout", Message: "upstream call timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &toolError{Code: "canceled", Reason: "request_canceled", Message: "request was canceled"}
	}
	st, ok := status.FromError(err)
	if !ok {
		return &toolError{Code: "internal", Reason: "upstream_failure", Message: "upstream call failed"}
	}
	reason, field, upCode, upReason := "", "", "", ""
	for _, detail := range st.Details() {
		switch d := detail.(type) {
		case *dv1.ErrorDetail:
			reason = d.GetReason()
		case *av1.ErrorDetail:
			reason, field, upCode, upReason = d.GetReason(), d.GetField(), d.GetUpstreamCode(), d.GetUpstreamReason()
		}
	}
	if reason == "" {
		switch st.Code() {
		case codes.DeadlineExceeded:
			reason = "request_timeout"
		case codes.Canceled:
			reason = "request_canceled"
		case codes.Unavailable:
			reason = "upstream_unavailable"
		default:
			reason = "upstream_failure"
		}
	}
	message := st.Message()
	if len(message) > 512 {
		message = message[:512]
	}
	return &toolError{Code: grpcCodeName(st.Code()), Reason: reason, Message: message, Field: field, UpstreamCode: upCode, UpstreamReason: upReason}
}

func grpcCodeName(code codes.Code) string {
	var b strings.Builder
	for i, r := range code.String() {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func (a *Adapter) result(value map[string]any, err error) *mcp.CallToolResult {
	if err != nil {
		var e *toolError
		if !errors.As(err, &e) {
			e = upstream(err)
		}
		b, _ := json.Marshal(e)
		result := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
		encoded, _ := json.Marshal(result)
		if len(encoded) > a.maxBytes {
			fallback := &toolError{Code: "resource_exhausted", Reason: "error_response_too_large", Message: "upstream error exceeds configured byte limit"}
			b, _ = json.Marshal(fallback)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
		}
		return result
	}
	value["served_at"] = a.now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(value)
	if err != nil {
		return a.result(nil, &toolError{Code: "internal", Reason: "internal_error", Message: "cannot encode MCP result"})
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		return a.result(nil, &toolError{Code: "internal", Reason: "internal_error", Message: "cannot encode MCP result"})
	}
	if len(encoded) > a.maxBytes {
		return a.result(nil, &toolError{Code: "resource_exhausted", Reason: "result_too_large", Message: "MCP result exceeds configured byte limit; request a smaller range"})
	}
	return result
}

func (a *Adapter) snapshot(ctx context.Context, kind string, in identity) *mcp.CallToolResult {
	if err := in.validate(); err != nil {
		return a.result(nil, err)
	}
	ctx, cancel := a.context(ctx)
	defer cancel()
	var msg proto.Message
	var err error
	switch kind {
	case "instrument":
		msg, err = a.data.ListInstruments(ctx, &dv1.ListInstrumentsRequest{Exchange: &in.Exchange, Market: &in.Market, Symbol: &in.Symbol})
	case "ticker":
		msg, err = a.data.ListTickers(ctx, &dv1.ListTickersRequest{Exchange: &in.Exchange, Market: &in.Market, Symbol: &in.Symbol})
	case "market_stats":
		msg, err = a.data.ListMarketStats(ctx, &dv1.ListMarketStatsRequest{Exchange: &in.Exchange, Market: &in.Market, Symbol: &in.Symbol})
	}
	if err != nil {
		return a.result(nil, err)
	}
	m, err := protoMap(msg)
	if err != nil {
		return a.result(nil, &toolError{Code: "data_loss", Reason: "invalid_upstream_response", Message: "cannot encode upstream response"})
	}
	key := map[string]string{"instrument": "instruments", "ticker": "tickers", "market_stats": "market_stats"}[kind]
	rows, _ := m[key].([]any)
	if len(rows) == 0 {
		return a.result(nil, &toolError{Code: "not_found", Reason: "symbol_not_found", Message: "no matching snapshot row"})
	}
	if len(rows) != 1 {
		return a.result(nil, &toolError{Code: "data_loss", Reason: "unexpected_upstream_result", Message: "upstream returned more than one row for an exact symbol"})
	}
	result := map[string]any{kind: rows[0]}
	if kind == "ticker" {
		result["snapshot_type"] = "cached_ticker"
	}
	return a.result(result, nil)
}

func (a *Adapter) candles(ctx context.Context, in candleArgs) *mcp.CallToolResult {
	req, err := in.request()
	if err != nil {
		return a.result(nil, err)
	}
	ctx, cancel := a.context(ctx)
	defer cancel()
	resp, err := a.data.GetKlines(ctx, req)
	if err != nil {
		return a.result(nil, err)
	}
	m, err := protoMap(resp)
	if err != nil {
		return a.result(nil, &toolError{Code: "data_loss", Reason: "invalid_upstream_response", Message: "cannot encode upstream response"})
	}
	m["source_range"] = map[string]any{"from": in.From, "to": in.To, "end_exclusive": true}
	return a.result(m, nil)
}

func (a *Adapter) analysis(ctx context.Context, call func(context.Context) (proto.Message, error)) *mcp.CallToolResult {
	ctx, cancel := a.context(ctx)
	defer cancel()
	resp, err := call(ctx)
	if err != nil {
		return a.result(nil, err)
	}
	m, err := protoMap(resp)
	if err != nil {
		return a.result(nil, &toolError{Code: "data_loss", Reason: "invalid_upstream_response", Message: "cannot encode upstream response"})
	}
	candles, _ := m["candles"].([]any)
	delete(m, "candles")
	latest := ""
	for _, row := range candles {
		if fetched, ok := row.(map[string]any)["fetched_at"].(string); ok && fetched > latest {
			latest = fetched
		}
	}
	source := map[string]any{"omitted": true, "omitted_count": len(candles)}
	if latest != "" {
		source["latest_fetched_at"] = latest
	}
	m["source_candles"] = source
	return a.result(m, nil)
}

func (a *Adapter) atr(ctx context.Context, in atrArgs, normalized bool) *mcp.CallToolResult {
	selection, err := in.selectionArgs.request()
	if err != nil {
		return a.result(nil, err)
	}
	if in.Period == 0 || in.Period >= in.CandleCount {
		return a.result(nil, invalid("period", "period must be positive and less than candle_count"))
	}
	settings := &av1.ATRSettings{Period: &in.Period}
	if normalized {
		req := &av1.GetNATRRequest{Selection: selection, Settings: settings}
		return a.analysis(ctx, func(ctx context.Context) (proto.Message, error) { return a.analyzer.GetNATR(ctx, req) })
	}
	req := &av1.GetATRRequest{Selection: selection, Settings: settings}
	return a.analysis(ctx, func(ctx context.Context) (proto.Message, error) { return a.analyzer.GetATR(ctx, req) })
}

func (a *Adapter) extrema(ctx context.Context, in extremaArgs) *mcp.CallToolResult {
	selection, err := in.selectionArgs.request()
	if err != nil {
		return a.result(nil, err)
	}
	settings, err := in.settings()
	if err != nil {
		return a.result(nil, err)
	}
	req := &av1.GetExtremaRequest{Selection: selection, Settings: settings}
	return a.analysis(ctx, func(ctx context.Context) (proto.Message, error) { return a.analyzer.GetExtrema(ctx, req) })
}

func (a *Adapter) trend(ctx context.Context, in trendArgs) *mcp.CallToolResult {
	selection, err := in.selectionArgs.request()
	if err != nil {
		return a.result(nil, err)
	}
	extrema, err := in.extremaArgs.settings()
	if err != nil {
		return a.result(nil, err)
	}
	if err := decimal("equality_tolerance_pct", in.EqualityTolerancePct, true); err != nil {
		return a.result(nil, err)
	}
	req := &av1.GetTrendRequest{Selection: selection, Settings: &av1.TrendSettings{Extrema: extrema, EqualityTolerancePct: &in.EqualityTolerancePct}}
	return a.analysis(ctx, func(ctx context.Context) (proto.Message, error) { return a.analyzer.GetTrend(ctx, req) })
}

func (a *Adapter) levels(ctx context.Context, in levelsArgs) *mcp.CallToolResult {
	selection, err := in.selectionArgs.request()
	if err != nil {
		return a.result(nil, err)
	}
	extrema, err := in.extremaArgs.settings()
	if err != nil {
		return a.result(nil, err)
	}
	if in.ZoneATRPeriod == 0 {
		return a.result(nil, invalid("zone_atr_period", "zone_atr_period must be positive"))
	}
	if err := decimal("zone_width_atr", in.ZoneWidthATR, false); err != nil {
		return a.result(nil, err)
	}
	if in.MinTouches < 2 {
		return a.result(nil, invalid("min_touches", "min_touches must be at least two"))
	}
	if in.MinTouchSeparationBars == 0 {
		return a.result(nil, invalid("min_touch_separation_bars", "min_touch_separation_bars must be positive"))
	}
	settings := &av1.LevelSettings{Extrema: extrema, AtrPeriod: &in.ZoneATRPeriod, ZoneWidthAtr: &in.ZoneWidthATR, MinTouches: &in.MinTouches, MinTouchSeparationBars: &in.MinTouchSeparationBars}
	req := &av1.GetLevelsRequest{Selection: selection, Settings: settings}
	return a.analysis(ctx, func(ctx context.Context) (proto.Message, error) { return a.analyzer.GetLevels(ctx, req) })
}

func (a *Adapter) findActiveInstruments(ctx context.Context, in activeArgs) *mcp.CallToolResult {
	req, err := in.request()
	if err != nil {
		return a.result(nil, err)
	}
	ctx, cancel := a.context(ctx)
	defer cancel()

	resp, err := a.analyzer.FindActiveInstruments(ctx, req)
	if err != nil {
		return a.result(nil, err)
	}
	m, err := protoMap(resp)
	if err != nil {
		return a.result(nil, &toolError{
			Code:    "data_loss",
			Reason:  "invalid_upstream_response",
			Message: "cannot encode upstream response",
		})
	}
	return a.result(m, nil)
}
