// Package pricing evaluates frozen sale prices against confirmed usage only.
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

type Rates struct {
	Input        string `json:"input_price"`
	Output       string `json:"output_price"`
	CacheRead    string `json:"cache_read_price"`
	CacheWrite   string `json:"cache_write_price"`
	CacheWrite1h string `json:"cache_write_1h_price"`
}
type Tier struct {
	Name string `json:"name"`
	// Inclusive bound. Zero is the final, unbounded tier.
	MaxInput int64 `json:"max_input_tokens"`
	Rates
}
type Config struct {
	Mode       string `json:"mode,omitempty"`
	Tiers      []Tier `json:"tiers,omitempty"`
	Expression string `json:"expression,omitempty"`
	// Explicit USDC per actual invocation. Missing means the tool is unavailable.
	ToolPrices map[string]string `json:"tool_prices,omitempty"`
}
type Facts struct {
	Input        int64            `json:"input_tokens"`
	Output       int64            `json:"output_tokens"`
	CacheRead    int64            `json:"cache_read_tokens"`
	CacheWrite   int64            `json:"cache_write_tokens"`
	CacheWrite1h int64            `json:"cache_write_1h_tokens"`
	Tools        map[string]int64 `json:"tool_calls,omitempty"`
	Parameters   map[string]any   `json:"parameters,omitempty"`
}
type Trace struct {
	AppliedRates   *Rates `json:"applied_rates,omitempty"`
	Expression     string `json:"expression,omitempty"`
	Mode           string `json:"mode"`
	MatchedTier    string `json:"matched_tier,omitempty"`
	ExpressionHash string `json:"expression_hash,omitempty"`
	Facts          Facts  `json:"facts"`
	TokenCost      string `json:"token_cost"`
	ToolCost       string `json:"tool_cost"`
	DiscountBPS    int    `json:"discount_bps"`
	Amount         string `json:"amount"`
}

func Price(raw string) (decimal.Decimal, error) {
	n, e := decimal.NewFromString(raw)
	if e != nil || n.IsNegative() || n.Exponent() < -12 || n.GreaterThan(decimal.NewFromInt(1_000_000_000)) {
		return decimal.Zero, fmt.Errorf("price must be a non-negative decimal, at most 12 decimal places and 1000000000")
	}
	return n, nil
}
func (r Rates) values() []string {
	return []string{r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CacheWrite1h}
}
func (r Rates) Validate() error {
	for _, raw := range r.values() {
		if _, e := Price(raw); e != nil {
			return e
		}
	}
	return nil
}
func (c Config) Validate() error {
	if !slices.Contains([]string{"", "standard", "tiered", "expression"}, c.Mode) {
		return fmt.Errorf("invalid pricing mode")
	}
	if len(c.ToolPrices) > 2 {
		return fmt.Errorf("unsupported tool pricing")
	}
	for name, raw := range c.ToolPrices {
		if name != "web_search" && name != "file_search" {
			return fmt.Errorf("unsupported priced tool: %s", name)
		}
		if _, e := Price(raw); e != nil {
			return e
		}
	}
	if len(c.Tiers) > 16 {
		return fmt.Errorf("at most 16 pricing tiers")
	}
	names := map[string]bool{}
	for i, t := range c.Tiers {
		if strings.TrimSpace(t.Name) == "" || len(t.Name) > 80 || names[t.Name] {
			return fmt.Errorf("tier names must be nonempty and unique")
		}
		names[t.Name] = true
		if t.MaxInput < 0 || i < len(c.Tiers)-1 && t.MaxInput == 0 || i > 0 && t.MaxInput != 0 && t.MaxInput <= c.Tiers[i-1].MaxInput {
			return fmt.Errorf("tier bounds must increase; only the last may be unbounded")
		}
		if e := t.Rates.Validate(); e != nil {
			return e
		}
	}
	if c.Mode == "tiered" && (len(c.Tiers) == 0 || c.Tiers[len(c.Tiers)-1].MaxInput != 0) {
		return fmt.Errorf("tiered pricing requires a final unbounded tier")
	}
	if c.Mode == "expression" || c.Expression != "" {
		if _, e := compile(c.Expression); e != nil {
			return e
		}
	}
	return nil
}
func (c Config) Billable(base Rates) bool {
	if c.Mode == "expression" {
		return true
	}
	values := base.values()
	if c.Mode == "tiered" {
		values = nil
		for _, t := range c.Tiers {
			values = append(values, t.Rates.values()...)
		}
	}
	for _, v := range c.ToolPrices {
		values = append(values, v)
	}
	for _, v := range values {
		n, e := decimal.NewFromString(v)
		if e != nil || n.IsPositive() {
			return true
		}
	}
	return false
}
func Charge(c Config, base Rates, f Facts, discount int) (Trace, error) {
	trace := Trace{Mode: c.Mode, Facts: f, DiscountBPS: discount}
	if trace.Mode == "" {
		trace.Mode = "standard"
	}
	if e := c.Validate(); e != nil {
		return trace, e
	}
	if e := base.Validate(); e != nil {
		return trace, e
	}
	if discount < 0 || discount > 10000 || f.Input < 0 || f.Output < 0 || f.CacheRead < 0 || f.CacheWrite < 0 || f.CacheWrite1h < 0 || f.CacheWrite1h > f.CacheWrite || f.CacheRead > f.Input || f.CacheWrite > f.Input-f.CacheRead {
		return trace, fmt.Errorf("invalid confirmed billing facts")
	}
	tokenCost := decimal.Zero
	switch trace.Mode {
	case "expression":
		trace.Expression = c.Expression
		program, e := compile(c.Expression)
		if e != nil {
			return trace, e
		}
		value, tier, e := program.run(f)
		if e != nil {
			return trace, e
		}
		tokenCost = value.Shift(-6)
		trace.MatchedTier = tier
		sum := sha256.Sum256([]byte(c.Expression))
		trace.ExpressionHash = hex.EncodeToString(sum[:])
	default:
		if trace.Mode == "tiered" {
			for _, t := range c.Tiers {
				if t.MaxInput == 0 || f.Input <= t.MaxInput {
					base = t.Rates
					trace.MatchedTier = t.Name
					break
				}
			}
		}
		trace.AppliedRates = &base
		counts := []int64{f.Input - f.CacheRead - f.CacheWrite, f.Output, f.CacheRead, f.CacheWrite - f.CacheWrite1h, f.CacheWrite1h}
		for i, raw := range base.values() {
			price, _ := Price(raw)
			tokenCost = tokenCost.Add(price.Mul(decimal.NewFromInt(counts[i])).Shift(-6))
		}
	}
	tools := decimal.Zero
	for name, count := range f.Tools {
		if count < 0 || count > 10000 {
			return trace, fmt.Errorf("invalid actual tool count")
		}
		if count == 0 {
			continue
		}
		raw, ok := c.ToolPrices[name]
		if !ok {
			return trace, fmt.Errorf("actual tool has no configured price: %s", name)
		}
		price, _ := Price(raw)
		tools = tools.Add(price.Mul(decimal.NewFromInt(count)))
	}
	amount := tokenCost.Add(tools).Mul(decimal.NewFromInt(int64(10000 - discount))).Shift(-4).Round(18)
	if tokenCost.IsNegative() || amount.IsNegative() || amount.GreaterThanOrEqual(decimal.NewFromInt(1_000_000_000_000)) {
		return trace, fmt.Errorf("pricing result out of range")
	}
	trace.TokenCost = tokenCost.String()
	trace.ToolCost = tools.String()
	trace.Amount = amount.String()
	return trace, nil
}
