package integrations

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The Python module wraps the yfinance library. yfinance itself reads Yahoo's
// public chart, quoteSummary and search endpoints; this port speaks those
// endpoints directly so a fake upstream can exercise every action. quoteSummary
// is a secondary source: when Yahoo rejects it the action still returns the
// chart-backed fields instead of failing (yfinance degrades the same way).

func yahooSymbol(input map[string]any) string {
	return strings.ToUpper(strings.TrimSpace(stringArg(input, "symbol")))
}

// yahooBase returns the configured base; quoteSummary lives on query2, so the
// historical default host is rewritten while injected test bases pass through.
func yahooBase(rt *Runtime, secondary bool) string {
	base := batch2BaseURL(rt, "yahoo_finance_tool", nil)
	if secondary {
		base = strings.Replace(base, "query1.", "query2.", 1)
	}
	return base
}

// yahooValue unwraps quoteSummary's {"raw":..,"fmt":..} cells.
func yahooValue(value any) any {
	if object, ok := value.(map[string]any); ok {
		if raw, present := object["raw"]; present {
			return raw
		}
	}
	return value
}

func yahooObject(root map[string]any, key string) map[string]any {
	return batch2Object(root, key)
}

// yahooChartResult fetches the chart endpoint and returns result[0].
func yahooChartResult(ctx context.Context, rt *Runtime, symbol string, params url.Values) (map[string]any, map[string]any) {
	target := yahooBase(rt, false) + "/v8/finance/chart/" + url.PathEscape(symbol) + "?" + params.Encode()
	parsed, problem := batch2GetObject(ctx, rt, target, nil)
	if problem != nil {
		return nil, problem
	}
	chart := yahooObject(parsed, "chart")
	if description := yahooErrorDescription(chart); description != "" {
		return nil, map[string]any{"error": description}
	}
	results, _ := chart["result"].([]any)
	if len(results) == 0 {
		return nil, map[string]any{"error": fmt.Sprintf("No data found for symbol '%s'", symbol)}
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		return nil, map[string]any{"error": fmt.Sprintf("No data found for symbol '%s'", symbol)}
	}
	return result, nil
}

func yahooErrorDescription(chart map[string]any) string {
	errorObject, ok := chart["error"].(map[string]any)
	if !ok {
		return ""
	}
	description := stringArg(errorObject, "description")
	if description == "" {
		description = stringArg(errorObject, "code")
	}
	return description
}

// yahooQuoteSummary fetches selected quoteSummary modules. Failures are
// reported as an empty map: callers only use it to enrich chart data.
func yahooQuoteSummary(ctx context.Context, rt *Runtime, symbol string, modules string) map[string]any {
	params := url.Values{}
	params.Set("modules", modules)
	target := yahooBase(rt, true) + "/v10/finance/quoteSummary/" + url.PathEscape(symbol) + "?" + params.Encode()
	parsed, problem := batch2GetObject(ctx, rt, target, nil)
	if problem != nil {
		return map[string]any{}
	}
	summary := yahooObject(parsed, "quoteSummary")
	results, _ := summary["result"].([]any)
	if len(results) == 0 {
		return map[string]any{}
	}
	result, ok := results[0].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return result
}

func yahooModuleValue(modules map[string]any, module, key string) any {
	block, ok := modules[module].(map[string]any)
	if !ok {
		return nil
	}
	value, ok := block[key]
	if !ok {
		return nil
	}
	unwrapped := yahooValue(value)
	if unwrapped == nil {
		return nil
	}
	return unwrapped
}

func yahooString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func yahooFirst(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		return value
	}
	return nil
}

func yahooStringField(modules map[string]any, module, key string) string {
	return stringArg(map[string]any{"value": yahooModuleValue(modules, module, key)}, "value")
}

func yahooFinanceQuote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	symbol := yahooSymbol(input)
	if symbol == "" {
		return map[string]any{"error": "symbol is required"}, nil
	}
	params := url.Values{}
	params.Set("range", "1d")
	params.Set("interval", "1d")
	result, problem := yahooChartResult(ctx, rt, symbol, params)
	if problem != nil {
		return map[string]any{"error": "Failed to fetch quote for " + symbol + ": " + stringArg(problem, "error")}, nil
	}
	meta := yahooObject(result, "meta")
	price := yahooModuleValue(map[string]any{"price": meta}, "price", "regularMarketPrice")
	if price == nil {
		return map[string]any{"error": fmt.Sprintf("No data found for symbol '%s'", symbol)}, nil
	}
	summary := yahooQuoteSummary(ctx, rt, symbol, "price,summaryDetail,defaultKeyStatistics")

	value := func(module, key string) any { return yahooModuleValue(summary, module, key) }
	return map[string]any{
		"symbol":         symbol,
		"name":           yahooString(yahooFirst(yahooStringField(summary, "price", "shortName"), stringArg(meta, "shortName"))),
		"price":          price,
		"previous_close": yahooFirst(meta["chartPreviousClose"], meta["previousClose"], value("price", "regularMarketPreviousClose")),
		"open":           yahooFirst(value("price", "regularMarketOpen"), meta["regularMarketOpen"]),
		"day_high":       yahooFirst(meta["regularMarketDayHigh"], value("price", "regularMarketDayHigh")),
		"day_low":        yahooFirst(meta["regularMarketDayLow"], value("price", "regularMarketDayLow")),
		"volume":         yahooFirst(meta["regularMarketVolume"], value("price", "regularMarketVolume")),
		"market_cap":     value("price", "marketCap"),
		"pe_ratio":       yahooFirst(value("summaryDetail", "trailingPE"), value("defaultKeyStatistics", "trailingPE")),
		"eps":            value("defaultKeyStatistics", "trailingEps"),
		"dividend_yield": value("summaryDetail", "dividendYield"),
		"52w_high":       value("summaryDetail", "fiftyTwoWeekHigh"),
		"52w_low":        value("summaryDetail", "fiftyTwoWeekLow"),
		"currency":       yahooString(yahooFirst(stringArg(meta, "currency"), value("price", "currency"))),
		"exchange":       yahooString(yahooFirst(stringArg(meta, "exchangeName"), meta["fullExchangeName"], value("price", "exchangeName"))),
	}, nil
}

func yahooFinanceHistory(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	symbol := yahooSymbol(input)
	if symbol == "" {
		return map[string]any{"error": "symbol is required"}, nil
	}
	period := strings.TrimSpace(stringArg(input, "period"))
	if period == "" {
		period = "1mo"
	}
	interval := strings.TrimSpace(stringArg(input, "interval"))
	if interval == "" {
		interval = "1d"
	}
	params := url.Values{}
	params.Set("range", period)
	params.Set("interval", interval)
	result, problem := yahooChartResult(ctx, rt, symbol, params)
	if problem != nil {
		return map[string]any{"error": fmt.Sprintf("Failed to fetch history for %s: %s", symbol, stringArg(problem, "error"))}, nil
	}
	timestamps, _ := result["timestamp"].([]any)
	indicators := yahooObject(result, "indicators")
	quotes, _ := indicators["quote"].([]any)
	quote := map[string]any{}
	if len(quotes) > 0 {
		quote, _ = quotes[0].(map[string]any)
	}
	column := func(name string) []any {
		values, _ := quote[name].([]any)
		return values
	}
	opens, highs, lows, closes, volumes := column("open"), column("high"), column("low"), column("close"), column("volume")
	if len(timestamps) == 0 {
		return map[string]any{"error": fmt.Sprintf("No historical data for '%s' with period=%s", symbol, period)}, nil
	}
	at := func(values []any, index int) float64 {
		if index >= len(values) {
			return 0
		}
		number, _ := batch2Number(values[index])
		return number
	}
	data := make([]map[string]any, 0, len(timestamps))
	for index, raw := range timestamps {
		stamp, ok := batch2Number(raw)
		if !ok {
			continue
		}
		data = append(data, map[string]any{
			"date":   time.Unix(int64(stamp), 0).UTC().Format("2006-01-02"),
			"open":   batch2Round(at(opens, index)),
			"high":   batch2Round(at(highs, index)),
			"low":    batch2Round(at(lows, index)),
			"close":  batch2Round(at(closes, index)),
			"volume": int(at(volumes, index)),
		})
	}
	return map[string]any{"symbol": symbol, "period": period, "interval": interval, "data": data}, nil
}

// yahooStatementModule maps the upstream statement name to quoteSummary data.
func yahooStatementModule(statement string) string {
	switch statement {
	case "income":
		return "incomeStatementHistory"
	case "balance":
		return "balanceSheetHistory"
	case "cashflow":
		return "cashflowStatementHistory"
	default:
		return ""
	}
}

func yahooFinanceFinancials(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	symbol := yahooSymbol(input)
	if symbol == "" {
		return map[string]any{"error": "symbol is required"}, nil
	}
	statement := strings.TrimSpace(stringArg(input, "statement"))
	if statement == "" {
		statement = "income"
	}
	module := yahooStatementModule(statement)
	if module == "" {
		return map[string]any{"error": fmt.Sprintf("Invalid statement type: %s. Use: income, balance, cashflow", statement)}, nil
	}
	summary := yahooQuoteSummary(ctx, rt, symbol, module)
	block, ok := summary[module].(map[string]any)
	if !ok {
		return map[string]any{"error": fmt.Sprintf("No %s statement data for '%s'", statement, symbol)}, nil
	}
	statements, _ := block["statements"].([]any)
	if len(statements) == 0 {
		return map[string]any{"error": fmt.Sprintf("No %s statement data for '%s'", statement, symbol)}, nil
	}
	data := map[string]any{}
	for index, raw := range statements {
		if index >= 4 {
			break
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		endDate, _ := entry["endDate"].(map[string]any)
		period := stringArg(endDate, "fmt")
		if period == "" {
			if stamp, ok := batch2Number(yahooValue(endDate["raw"])); ok {
				period = time.Unix(int64(stamp), 0).UTC().Format("2006-01-02")
			}
		}
		fields := map[string]any{}
		for key, value := range entry {
			if key == "maxAge" || key == "endDate" {
				continue
			}
			unwrapped := yahooValue(value)
			if unwrapped == nil {
				continue
			}
			if number, ok := batch2Number(unwrapped); ok {
				fields[key] = number
				continue
			}
			fields[key] = unwrapped
		}
		data[period] = fields
	}
	return map[string]any{"symbol": symbol, "statement": statement, "data": data}, nil
}

func yahooFinanceInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	symbol := yahooSymbol(input)
	if symbol == "" {
		return map[string]any{"error": "symbol is required"}, nil
	}
	summary := yahooQuoteSummary(ctx, rt, symbol, "assetProfile,price")
	name := yahooStringField(summary, "price", "shortName")
	if name == "" {
		return map[string]any{"error": fmt.Sprintf("No info found for symbol '%s'", symbol)}, nil
	}
	description := yahooStringField(summary, "assetProfile", "longBusinessSummary")
	if len(description) > 1000 {
		description = description[:1000] + "..."
	}
	return map[string]any{
		"symbol":      symbol,
		"name":        name,
		"long_name":   yahooStringField(summary, "price", "longName"),
		"sector":      yahooStringField(summary, "assetProfile", "sector"),
		"industry":    yahooStringField(summary, "assetProfile", "industry"),
		"description": description,
		"website":     yahooStringField(summary, "assetProfile", "website"),
		"employees":   yahooModuleValue(summary, "assetProfile", "fullTimeEmployees"),
		"country":     yahooStringField(summary, "assetProfile", "country"),
		"city":        yahooStringField(summary, "assetProfile", "city"),
		"address":     yahooStringField(summary, "assetProfile", "address1"),
	}, nil
}

func yahooFinanceSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(input, "query"))
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("quotesCount", "20")
	params.Set("newsCount", "0")
	target := yahooBase(rt, false) + "/v1/finance/search?" + params.Encode()
	parsed, problem := batch2GetObject(ctx, rt, target, nil)
	if problem != nil {
		return map[string]any{"error": "Search failed: " + stringArg(problem, "error")}, nil
	}
	quotes, _ := parsed["quotes"].([]any)
	results := make([]map[string]any, 0, len(quotes))
	for index, raw := range quotes {
		if index >= 20 {
			break
		}
		quote, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := stringArg(quote, "shortname")
		if name == "" {
			name = stringArg(quote, "longname")
		}
		results = append(results, map[string]any{
			"symbol":   stringArg(quote, "symbol"),
			"name":     name,
			"exchange": stringArg(quote, "exchange"),
			"type":     stringArg(quote, "quoteType"),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}
