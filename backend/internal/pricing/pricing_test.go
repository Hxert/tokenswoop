package pricing

import (
	"strings"
	"testing"
)

func baseRates() Rates {
	return Rates{Input: "2", Output: "8", CacheRead: "0.2", CacheWrite: "2.5", CacheWrite1h: "4"}
}
func TestConfirmedPricing(t *testing.T) {
	facts := Facts{Input: 1000, Output: 100, CacheRead: 200, CacheWrite: 100, CacheWrite1h: 40}
	for _, tc := range []struct {
		name     string
		c        Config
		discount int
		want     string
	}{
		{"standard", Config{}, 0, "0.00255"},
		{"discount", Config{}, 1000, "0.002295"},
		{"expression cache split", Config{Mode: "expression", Expression: `v1:p*2+c*8+cr*0.2+cc*2.5+cc1h*4`}, 0, "0.00255"},
		{"cache unaware", Config{Mode: "expression", Expression: `p*2+c*8`}, 0, "0.0028"},
		{"fixed", Config{Mode: "expression", Expression: `fixed(0.000000000001)`}, 0, "0.000000000001"},
		{"conditional", Config{Mode: "expression", Expression: `len<=1000 ? tier("short",p*2+c*8) : tier("long",p*4+c*12)`}, 0, "0.0028"},
		{"numeric helpers", Config{Mode: "expression", Expression: `min(4,max(2,ceil(1.1)))+floor(1.9)+abs(-1)`}, 0, "0.000004"},
		{"effective parameters", Config{Mode: "expression", Expression: `param("service_tier")=="priority" ? p*4 : p*2`}, 0, "0.002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace, e := Charge(tc.c, baseRates(), facts, tc.discount)
			if e != nil || trace.Amount != tc.want {
				t.Fatalf("%+v %v; want %s", trace, e, tc.want)
			}
		})
	}
	config := Config{Mode: "tiered", Tiers: []Tier{{Name: "small", MaxInput: 1000, Rates: baseRates()}, {Name: "large", Rates: Rates{Input: "4", Output: "12", CacheRead: "0.4", CacheWrite: "5", CacheWrite1h: "8"}}}}
	for _, input := range []int64{999, 1000, 1001} {
		facts.Input = input
		trace, e := Charge(config, baseRates(), facts, 0)
		want := "small"
		if input == 1001 {
			want = "large"
		}
		if e != nil || trace.MatchedTier != want {
			t.Fatalf("input %d: %+v %v", input, trace, e)
		}
	}
}
func TestInvalidRulesCannotBeSaved(t *testing.T) {
	for _, expr := range []string{`p + "string"`, `true ? p : "string"`, `"free"`, `unknown*1`, `map([1],#)`, `param("authorization")`, `fixed(p)`, `tier(p,1)`, `p**2`, `[p]`, `p.foo`, `len ? p : c`, strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40) + strings.Repeat("+1", 520)} {
		if e := (Config{Mode: "expression", Expression: expr}).Validate(); e == nil {
			t.Fatalf("invalid expression accepted: %s", expr)
		}
	}
	for _, config := range []Config{{Mode: "tiered"}, {Mode: "tiered", Tiers: []Tier{{Name: "incomplete", MaxInput: 100, Rates: baseRates()}}}, {ToolPrices: map[string]string{"computer_use": "1"}}} {
		if e := config.Validate(); e == nil {
			t.Fatal("invalid config accepted")
		}
	}
	for _, f := range []Facts{{Input: 1, CacheRead: 2}, {Input: 1, CacheWrite: 1, CacheWrite1h: 2}, {Input: -1}, {Tools: map[string]int64{"web_search": 1}}} {
		if _, e := Charge(Config{}, baseRates(), f, 0); e == nil {
			t.Fatal("invalid facts accepted")
		}
	}
	for _, expr := range []string{`p/0`, `-p`} {
		if _, e := Charge(Config{Mode: "expression", Expression: expr}, baseRates(), Facts{Input: 1}, 0); e == nil {
			t.Fatal("invalid runtime result charged")
		}
	}
}
func TestToolCallsAndDecimalPrecision(t *testing.T) {
	trace, e := Charge(Config{ToolPrices: map[string]string{"web_search": "0.01", "file_search": "0.0025"}}, baseRates(), Facts{Tools: map[string]int64{"web_search": 2, "file_search": 3}}, 1000)
	if e != nil || trace.Amount != "0.02475" || trace.ToolCost != "0.0275" {
		t.Fatalf("%+v %v", trace, e)
	}
	trace, e = Charge(Config{Mode: "expression", Expression: `p*0.000000000001`}, baseRates(), Facts{Input: 1}, 0)
	if e != nil || trace.Amount != "0.000000000000000001" {
		t.Fatalf("lost precision: %+v %v", trace, e)
	}
}

func TestLongContextPricingWithoutTokenCeilings(t *testing.T) {
	c := Config{Mode: "tiered", Tiers: []Tier{{Name: "large", MaxInput: 20000000, Rates: baseRates()}, {Name: "larger", Rates: baseRates()}}}
	trace, e := Charge(c, baseRates(), Facts{Input: 12000000, Output: 300000}, 0)
	if e != nil || trace.MatchedTier != "large" || trace.Amount != "26.4" {
		t.Fatalf("long context was limited: %+v %v", trace, e)
	}
	if _, e = Charge(Config{}, baseRates(), Facts{Input: 9223372036854775807, CacheRead: 9223372036854775807, CacheWrite: 1}, 0); e == nil {
		t.Fatal("overflowing cache total accepted")
	}
}
