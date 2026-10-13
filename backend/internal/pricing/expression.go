package pricing

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/parser"
	"github.com/shopspring/decimal"
)

// New API v1 token variables, tier/fixed markers and conditional syntax, with
// a decimal-only evaluator. No VM, loops, reflection, I/O or request-body access.
type expression struct {
	root   ast.Node
	source []rune
	used   map[string]bool
}

var expressions = struct {
	sync.RWMutex
	entries map[string]*expression
}{entries: map[string]*expression{}}
var parameters = []string{"service_tier", "reasoning_effort", "max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p"}

func compile(raw string) (*expression, error) {
	expressions.RLock()
	cached := expressions.entries[raw]
	expressions.RUnlock()
	if cached != nil {
		return cached, nil
	}
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, fmt.Errorf("expression must contain 1–4096 bytes")
	}
	body := strings.TrimPrefix(raw, "v1:")
	config := conf.CreateNew()
	config.MaxNodes = 512
	for name := range config.Builtins {
		config.Disabled[name] = true
	}
	tree, e := parser.ParseWithConfig(body, config)
	if e != nil {
		return nil, fmt.Errorf("invalid pricing expression: %w", e)
	}
	p := &expression{root: tree.Node, source: []rune(body), used: map[string]bool{}}
	nodes := 0
	var check func(ast.Node, int) error
	check = func(n ast.Node, depth int) error {
		nodes++
		if nodes > 512 || depth > 32 {
			return fmt.Errorf("pricing expression too complex")
		}
		children := []ast.Node{}
		switch v := n.(type) {
		case *ast.IntegerNode, *ast.FloatNode:
			if _, e := p.number(n); e != nil {
				return e
			}
		case *ast.StringNode:
			if len(v.Value) > 80 {
				return fmt.Errorf("expression string too long")
			}
		case *ast.BoolNode, *ast.NilNode:
		case *ast.IdentifierNode:
			if !slices.Contains([]string{"p", "c", "len", "cr", "cc", "cc1h"}, v.Value) {
				return fmt.Errorf("unsupported pricing variable %s", v.Value)
			}
			p.used[v.Value] = true
		case *ast.UnaryNode:
			if !slices.Contains([]string{"-", "+", "!", "not"}, v.Operator) {
				return fmt.Errorf("unsupported operator")
			}
			children = append(children, v.Node)
		case *ast.BinaryNode:
			if !slices.Contains([]string{"+", "-", "*", "/", "==", "!=", "<", "<=", ">", ">=", "&&", "||", "and", "or"}, v.Operator) {
				return fmt.Errorf("unsupported operator")
			}
			children = append(children, v.Left, v.Right)
		case *ast.ConditionalNode:
			children = append(children, v.Cond, v.Exp1, v.Exp2)
		case *ast.CallNode:
			fn, ok := v.Callee.(*ast.IdentifierNode)
			if !ok {
				return fmt.Errorf("invalid pricing function")
			}
			arity := map[string]int{"tier": 2, "fixed": 1, "min": 2, "max": 2, "abs": 1, "ceil": 1, "floor": 1, "param": 1}
			if arity[fn.Value] == 0 || len(v.Arguments) != arity[fn.Value] {
				return fmt.Errorf("unsupported pricing function or argument count")
			}
			if fn.Value == "param" {
				key, ok := v.Arguments[0].(*ast.StringNode)
				if !ok || !slices.Contains(parameters, key.Value) {
					return fmt.Errorf("param requires an allowed non-secret scalar name")
				}
			}
			if fn.Value == "tier" {
				label, ok := v.Arguments[0].(*ast.StringNode)
				if !ok || strings.TrimSpace(label.Value) == "" {
					return fmt.Errorf("tier requires a literal name")
				}
			}
			if fn.Value == "fixed" {
				if _, e := p.number(v.Arguments[0]); e != nil {
					return fmt.Errorf("fixed requires a non-negative numeric literal")
				}
			}
			children = append(children, v.Arguments...)
		default:
			return fmt.Errorf("unsupported expression syntax")
		}
		for _, child := range children {
			if e := check(child, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e = check(p.root, 0); e != nil {
		return nil, e
	}
	if kind, err := expressionKind(p.root); err != nil || kind != "number" {
		return nil, fmt.Errorf("pricing expression must be numeric with correctly typed branches: %v", err)
	}
	expressions.Lock()
	if len(expressions.entries) >= 256 {
		expressions.entries = map[string]*expression{}
	}
	expressions.entries[raw] = p
	expressions.Unlock()
	return p, nil
}

// Check both sides of conditionals before a price can be saved. Parameters have
// a known type; an absent numeric parameter evaluates to zero.
func expressionKind(n ast.Node) (string, error) {
	fail := func() (string, error) { return "", fmt.Errorf("incompatible expression operands") }
	switch v := n.(type) {
	case *ast.IntegerNode, *ast.FloatNode, *ast.IdentifierNode:
		return "number", nil
	case *ast.StringNode:
		return "string", nil
	case *ast.BoolNode:
		return "bool", nil
	case *ast.NilNode:
		return "nil", nil
	case *ast.UnaryNode:
		k, e := expressionKind(v.Node)
		if e != nil {
			return "", e
		}
		want := "number"
		if v.Operator == "!" || v.Operator == "not" {
			want = "bool"
		}
		if k != want {
			return fail()
		}
		return want, nil
	case *ast.BinaryNode:
		a, e := expressionKind(v.Left)
		if e != nil {
			return "", e
		}
		b, e := expressionKind(v.Right)
		if e != nil {
			return "", e
		}
		if v.Operator == "==" || v.Operator == "!=" {
			if a != b && a != "nil" && b != "nil" {
				return fail()
			}
			return "bool", nil
		}
		if slices.Contains([]string{"&&", "||", "and", "or"}, v.Operator) {
			if a != "bool" || b != "bool" {
				return fail()
			}
			return "bool", nil
		}
		if a != "number" || b != "number" {
			return fail()
		}
		if slices.Contains([]string{"<", "<=", ">", ">="}, v.Operator) {
			return "bool", nil
		}
		return "number", nil
	case *ast.ConditionalNode:
		k, e := expressionKind(v.Cond)
		if e != nil || k != "bool" {
			return fail()
		}
		a, e := expressionKind(v.Exp1)
		if e != nil {
			return "", e
		}
		b, e := expressionKind(v.Exp2)
		if e != nil || a != b {
			return fail()
		}
		return a, nil
	case *ast.CallNode:
		name := v.Callee.(*ast.IdentifierNode).Value
		if name == "param" {
			key := v.Arguments[0].(*ast.StringNode).Value
			if key == "service_tier" || key == "reasoning_effort" {
				return "string", nil
			}
			return "number", nil
		}
		args := v.Arguments
		if name == "tier" {
			args = args[1:]
		}
		for _, arg := range args {
			k, e := expressionKind(arg)
			if e != nil || k != "number" {
				return fail()
			}
		}
		return "number", nil
	}
	return fail()
}
func (p *expression) number(n ast.Node) (decimal.Decimal, error) {
	switch n.(type) {
	case *ast.IntegerNode, *ast.FloatNode:
	default:
		return decimal.Zero, fmt.Errorf("number required")
	}
	loc := n.Location()
	if loc.From < 0 || loc.To > len(p.source) || loc.From >= loc.To {
		return decimal.Zero, fmt.Errorf("invalid numeric literal")
	}
	raw := strings.ReplaceAll(string(p.source[loc.From:loc.To]), "_", "")
	value, e := decimal.NewFromString(raw)
	if e != nil || value.Exponent() < -18 || value.Abs().GreaterThan(decimal.NewFromInt(1_000_000_000_000)) {
		return decimal.Zero, fmt.Errorf("numeric literal out of range")
	}
	return value, nil
}
func (p *expression) run(f Facts) (decimal.Decimal, string, error) {
	prompt := f.Input
	if p.used["cr"] {
		prompt -= f.CacheRead
	}
	if p.used["cc"] {
		prompt -= f.CacheWrite - f.CacheWrite1h
	}
	if p.used["cc1h"] {
		prompt -= f.CacheWrite1h
	}
	vars := map[string]int64{"p": prompt, "c": f.Output, "len": f.Input, "cr": f.CacheRead, "cc": f.CacheWrite - f.CacheWrite1h, "cc1h": f.CacheWrite1h}
	tier := ""
	var eval func(ast.Node) (any, error)
	eval = func(n ast.Node) (any, error) {
		switch v := n.(type) {
		case *ast.IntegerNode, *ast.FloatNode:
			return p.number(n)
		case *ast.BoolNode:
			return v.Value, nil
		case *ast.StringNode:
			return v.Value, nil
		case *ast.NilNode:
			return nil, nil
		case *ast.IdentifierNode:
			return decimal.NewFromInt(vars[v.Value]), nil
		case *ast.ConditionalNode:
			cond, e := eval(v.Cond)
			if e != nil {
				return nil, e
			}
			b, ok := cond.(bool)
			if !ok {
				return nil, fmt.Errorf("boolean condition required")
			}
			if b {
				return eval(v.Exp1)
			}
			return eval(v.Exp2)
		case *ast.UnaryNode:
			value, e := eval(v.Node)
			if e != nil {
				return nil, e
			}
			if v.Operator == "!" || v.Operator == "not" {
				b, ok := value.(bool)
				if !ok {
					return nil, fmt.Errorf("boolean required")
				}
				return !b, nil
			}
			d, ok := value.(decimal.Decimal)
			if !ok {
				return nil, fmt.Errorf("number required")
			}
			if v.Operator == "-" {
				return d.Neg(), nil
			}
			return d, nil
		case *ast.BinaryNode:
			a, e := eval(v.Left)
			if e != nil {
				return nil, e
			}
			if slices.Contains([]string{"&&", "and", "||", "or"}, v.Operator) {
				b, ok := a.(bool)
				if !ok {
					return nil, fmt.Errorf("boolean required")
				}
				and := v.Operator == "&&" || v.Operator == "and"
				if and && !b || !and && b {
					return b, nil
				}
				right, e := eval(v.Right)
				if _, ok := right.(bool); !ok && e == nil {
					e = fmt.Errorf("boolean required")
				}
				return right, e
			}
			b, e := eval(v.Right)
			if e != nil {
				return nil, e
			}
			if v.Operator == "==" || v.Operator == "!=" {
				equal := false
				switch x := a.(type) {
				case decimal.Decimal:
					y, ok := b.(decimal.Decimal)
					equal = ok && x.Equal(y)
				case string:
					y, ok := b.(string)
					equal = ok && x == y
				case bool:
					y, ok := b.(bool)
					equal = ok && x == y
				case nil:
					equal = b == nil
				}
				if v.Operator == "!=" {
					equal = !equal
				}
				return equal, nil
			}
			x, ok := a.(decimal.Decimal)
			y, ok2 := b.(decimal.Decimal)
			if !ok || !ok2 {
				return nil, fmt.Errorf("numeric operands required")
			}
			switch v.Operator {
			case "+":
				return x.Add(y), nil
			case "-":
				return x.Sub(y), nil
			case "*":
				return x.Mul(y), nil
			case "/":
				if y.IsZero() {
					return nil, fmt.Errorf("division by zero")
				}
				return x.DivRound(y, 36), nil
			case "<":
				return x.LessThan(y), nil
			case "<=":
				return x.LessThanOrEqual(y), nil
			case ">":
				return x.GreaterThan(y), nil
			case ">=":
				return x.GreaterThanOrEqual(y), nil
			}
		case *ast.CallNode:
			name := v.Callee.(*ast.IdentifierNode).Value
			if name == "param" {
				key := v.Arguments[0].(*ast.StringNode).Value
				value := f.Parameters[key]
				if value == nil {
					if key == "service_tier" || key == "reasoning_effort" {
						return "", nil
					}
					return decimal.Zero, nil
				}
				switch x := value.(type) {
				case string, bool, nil:
					return value, nil
				default:
					n, e := decimal.NewFromString(fmt.Sprint(x))
					if e != nil {
						return nil, fmt.Errorf("invalid pricing parameter")
					}
					return n, nil
				}
			}
			if name == "tier" {
				tier = v.Arguments[0].(*ast.StringNode).Value
				return eval(v.Arguments[1])
			}
			args := []decimal.Decimal{}
			for _, a := range v.Arguments {
				value, e := eval(a)
				if e != nil {
					return nil, e
				}
				d, ok := value.(decimal.Decimal)
				if !ok {
					return nil, fmt.Errorf("numeric argument required")
				}
				args = append(args, d)
			}
			switch name {
			case "fixed":
				return args[0].Shift(6), nil
			case "abs":
				return args[0].Abs(), nil
			case "ceil":
				return args[0].Ceil(), nil
			case "floor":
				return args[0].Floor(), nil
			case "min":
				return decimal.Min(args[0], args[1]), nil
			case "max":
				return decimal.Max(args[0], args[1]), nil
			}
		}
		return nil, fmt.Errorf("invalid pricing expression")
	}
	result, e := eval(p.root)
	if e != nil {
		return decimal.Zero, tier, e
	}
	amount, ok := result.(decimal.Decimal)
	if !ok {
		return decimal.Zero, tier, fmt.Errorf("pricing expression must produce a number")
	}
	return amount, tier, nil
}
