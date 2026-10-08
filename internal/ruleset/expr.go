package ruleset

import (
	"fmt"
)

// Expression evaluator: tiny hand-rolled integer/boolean engine executing
// base-supplied data rules (derived stats, hook policies, symbol tables).
//
// Safety properties (spec §5, P05):
//   - No I/O: pure functions over the input string + variable scope.
//     This file must never import os/net/database/sql or similar.
//   - Bounds: MaxExprDepth (32) AST depth, MaxExprLiterals (256) literals,
//     64-bit checked arithmetic (overflow fails closed, never wraps).
//   - Unknown functions are errors, never silently ignored.
//
// Grammar:
//
//	expr    := ternary
//	ternary := or ("?" or ":" or)?            (right associative)
//	or      := and ("||" and)*
//	and     := cmp ("&&" cmp)*
//	cmp     := add (("=="|"!="|"<="|">="|"<"|">") add)?
//	add     := mul (("+"|"-") mul)*
//	mul     := unary (("*"|"/"|"%") unary)*
//	unary   := ("-"|"!") unary | primary
//	primary := number | ident | call | "(" expr ")" | "[" list "]"
//	          | "true" | "false"
//	call    := ident "(" expr ("," expr)* ")"
//	list    := expr ("," expr)*   (empty list is an error)
//
// Types are strict: arithmetic/comparison on ints, logic on bools,
// kh/kl/sum/count on int lists. "/" is truncated division (Go semantics);
// use floor(a,b) for floored division. Division by zero and signed
// overflow (including MinInt64/-1 and MinInt64 negation) are errors.
//
// Known functions (frozen set; anything else fails parse and lint):
//
//	min(a, b, ...)  smallest arg (ints, >=1 arg)
//	max(a, b, ...)  largest arg (ints, >=1 arg)
//	abs(a)          absolute value (errors on MinInt64)
//	floor(a)        identity on ints (kept for float-authored rules)
//	floor(a, b)     floored division (errors on b == 0, MinInt64/-1)
//	clamp(v, lo, hi)  pin v into [lo, hi] (errors if lo > hi)
//	sign(a)         -1, 0, or 1
//	if(c, a, b)     c must be bool; a and b must share a kind
//	kh(list, n)     keep highest n elements (returns list, desc)
//	kl(list, n)     keep lowest n elements (returns list, asc)
//	sum(list)       checked sum of elements
//	count(list)     number of elements
const (
	// MaxExprDepth bounds AST nesting (parens, calls, unary, binary).
	MaxExprDepth = 32
	// MaxExprLiterals bounds the number of int/bool literals per expression
	// (each element of a list literal counts).
	MaxExprLiterals = 256
	// MaxExprLen bounds input bytes (memory cap before parsing).
	MaxExprLen = 4096
)

// ValueKind is the runtime kind of a Value.
type ValueKind int

const (
	KindInt ValueKind = iota
	KindBool
	KindList
)

// Value is an evaluator datum: int64, bool, or []int64.
type Value struct {
	kind ValueKind
	i    int64
	b    bool
	list []int64
}

// IntValue builds an int Value.
func IntValue(v int64) Value { return Value{kind: KindInt, i: v} }

// BoolValue builds a bool Value.
func BoolValue(v bool) Value { return Value{kind: KindBool, b: v} }

// ListValue builds a list Value (copies the input).
func ListValue(v []int64) Value {
	cp := make([]int64, len(v))
	copy(cp, v)
	return Value{kind: KindList, list: cp}
}

// Kind reports the value kind.
func (v Value) Kind() ValueKind { return v.kind }

// Int returns the int payload (ok=false if not an int).
func (v Value) Int() (int64, bool) {
	if v.kind != KindInt {
		return 0, false
	}
	return v.i, true
}

// Bool returns the bool payload (ok=false if not a bool).
func (v Value) Bool() (bool, bool) {
	if v.kind != KindBool {
		return false, false
	}
	return v.b, true
}

// List returns a copy of the list payload (ok=false if not a list).
func (v Value) List() ([]int64, bool) {
	if v.kind != KindList {
		return nil, false
	}
	cp := make([]int64, len(v.list))
	copy(cp, v.list)
	return cp, true
}

func (v Value) String() string {
	switch v.kind {
	case KindInt:
		return fmt.Sprintf("%d", v.i)
	case KindBool:
		if v.b {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v.list)
	}
}

// knownFunctions is the frozen function registry. Unknown names fail
// parse (and therefore lint and eval) — never silently ignored.
var knownFunctions = map[string]bool{
	"min": true, "max": true, "abs": true, "floor": true,
	"clamp": true, "sign": true, "if": true,
	"kh": true, "kl": true, "sum": true, "count": true,
}

// IsKnownFunction reports whether name is in the frozen function set.
func IsKnownFunction(name string) bool { return knownFunctions[name] }

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tIdent
	tOp
	tLParen
	tRParen
	tLBrack
	tRBrack
	tComma
	tQMark
	tColon
)

type token struct {
	kind tokKind
	text string
	num  int64
}

func lex(input string) ([]token, error) {
	var out []token
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < len(input) && input[j] >= '0' && input[j] <= '9' {
				j++
			}
			var n uint64
			for k := i; k < j; k++ {
				n = n*10 + uint64(input[k]-'0')
				if n > 0x7FFFFFFFFFFFFFFF {
					return nil, fmt.Errorf("expr: integer literal overflows int64")
				}
			}
			out = append(out, token{kind: tNum, text: input[i:j], num: int64(n)})
			i = j
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i
			for j < len(input) && (input[j] == '_' ||
				(input[j] >= 'a' && input[j] <= 'z') ||
				(input[j] >= 'A' && input[j] <= 'Z') ||
				(input[j] >= '0' && input[j] <= '9')) {
				j++
			}
			out = append(out, token{kind: tIdent, text: input[i:j]})
			i = j
		case c == '(':
			out = append(out, token{kind: tLParen, text: "("})
			i++
		case c == ')':
			out = append(out, token{kind: tRParen, text: ")"})
			i++
		case c == '[':
			out = append(out, token{kind: tLBrack, text: "["})
			i++
		case c == ']':
			out = append(out, token{kind: tRBrack, text: "]"})
			i++
		case c == ',':
			out = append(out, token{kind: tComma, text: ","})
			i++
		case c == '?':
			out = append(out, token{kind: tQMark, text: "?"})
			i++
		case c == ':':
			out = append(out, token{kind: tColon, text: ":"})
			i++
		case c == '&' || c == '|' || c == '=' || c == '!' || c == '<' || c == '>' ||
			c == '+' || c == '-' || c == '*' || c == '/' || c == '%':
			two := ""
			if i+1 < len(input) {
				two = input[i : i+2]
			}
			switch two {
			case "&&", "||", "==", "!=", "<=", ">=":
				out = append(out, token{kind: tOp, text: two})
				i += 2
			default:
				if c == '=' {
					return nil, fmt.Errorf("expr: unexpected '=' (did you mean '==')?")
				}
				if c == '&' || c == '|' {
					return nil, fmt.Errorf("expr: unexpected %q (did you mean %q?)", string(c), string(c)+string(c))
				}
				out = append(out, token{kind: tOp, text: string(c)})
				i++
			}
		default:
			return nil, fmt.Errorf("expr: unexpected character %q", string(c))
		}
	}
	out = append(out, token{kind: tEOF})
	return out, nil
}

// ---- AST ----

type nodeKind int

const (
	nLit nodeKind = iota
	nVar
	nUnary
	nBinary
	nTern
	nCall
	nList
)

type node struct {
	kind     nodeKind
	val      Value   // nLit
	name     string  // nVar, nCall
	op       string  // nUnary, nBinary
	kids     []*node // operands / args / elements
	literals int     // literal count in subtree
	depth    int     // AST depth of subtree (leaf = 1)
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func finish(n *node, kids ...*node) (*node, error) {
	n.kids = kids
	n.literals = 0
	n.depth = 1
	for _, k := range kids {
		n.literals += k.literals
		if k.depth+1 > n.depth {
			n.depth = k.depth + 1
		}
	}
	if n.kind == nLit {
		if n.val.kind == KindList {
			n.literals += len(n.val.list)
		} else {
			n.literals++
		}
	}
	if n.depth > MaxExprDepth {
		return nil, fmt.Errorf("expr: exceeds max depth %d", MaxExprDepth)
	}
	if n.literals > MaxExprLiterals {
		return nil, fmt.Errorf("expr: exceeds max literals %d", MaxExprLiterals)
	}
	return n, nil
}

// ParseExpression parses input into an AST, enforcing depth/literal bounds
// and rejecting unknown function names. No evaluation happens here.
func ParseExpression(input string) (*node, error) {
	if len(input) > MaxExprLen {
		return nil, fmt.Errorf("expr: exceeds max length %d", MaxExprLen)
	}
	toks, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseTern()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("expr: unexpected trailing %q", p.peek().text)
	}
	return n, nil
}

func (p *parser) parseTern() (*node, error) {
	cond, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tQMark {
		return cond, nil
	}
	p.next()
	a, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tColon {
		return nil, fmt.Errorf("expr: expected ':' in ternary")
	}
	p.next()
	b, err := p.parseTern() // right associative
	if err != nil {
		return nil, err
	}
	return finish(&node{kind: nTern}, cond, a, b)
}

func (p *parser) parseOr() (*node, error) {
	return p.parseLeft(p.parseAnd, "||")
}

func (p *parser) parseAnd() (*node, error) {
	return p.parseLeft(p.parseCmp, "&&")
}

func (p *parser) parseCmp() (*node, error) {
	left, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tOp && (t.text == "==" || t.text == "!=" || t.text == "<" ||
		t.text == "<=" || t.text == ">" || t.text == ">=") {
		p.next()
		right, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		return finish(&node{kind: nBinary, op: t.text}, left, right)
	}
	return left, nil
}

func (p *parser) parseAdd() (*node, error) {
	return p.parseLeft(p.parseMul, "+", "-")
}

func (p *parser) parseMul() (*node, error) {
	return p.parseLeft(p.parseUnary, "*", "/", "%")
}

func (p *parser) parseLeft(sub func() (*node, error), ops ...string) (*node, error) {
	left, err := sub()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tOp || !containsOp(ops, t.text) {
			return left, nil
		}
		p.next()
		right, err := sub()
		if err != nil {
			return nil, err
		}
		left, err = finish(&node{kind: nBinary, op: t.text}, left, right)
		if err != nil {
			return nil, err
		}
	}
}

func containsOp(ops []string, op string) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

func (p *parser) parseUnary() (*node, error) {
	t := p.peek()
	if t.kind == tOp && (t.text == "-" || t.text == "!") {
		p.next()
		kid, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return finish(&node{kind: nUnary, op: t.text}, kid)
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (*node, error) {
	t := p.peek()
	switch t.kind {
	case tNum:
		p.next()
		return finish(&node{kind: nLit, val: IntValue(t.num)})
	case tIdent:
		p.next()
		if t.text == "true" || t.text == "false" {
			return finish(&node{kind: nLit, val: BoolValue(t.text == "true")})
		}
		if p.peek().kind == tLParen {
			if !IsKnownFunction(t.text) {
				return nil, fmt.Errorf("expr: unknown function %q", t.text)
			}
			p.next()
			var args []*node
			if p.peek().kind != tRParen {
				for {
					a, err := p.parseTern()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.peek().kind != tComma {
						break
					}
					p.next()
				}
			}
			if p.peek().kind != tRParen {
				return nil, fmt.Errorf("expr: expected ')' after arguments to %q", t.text)
			}
			p.next()
			return finish(&node{kind: nCall, name: t.text}, args...)
		}
		return finish(&node{kind: nVar, name: t.text})
	case tLParen:
		p.next()
		n, err := p.parseTern()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, fmt.Errorf("expr: expected ')'")
		}
		p.next()
		// Parentheses group without adding depth: splice the child through
		// so deep-but-flat nesting cannot evade MaxExprDepth... note the
		// child already enforces its own depth; grouping adds none.
		return n, nil
	case tLBrack:
		p.next()
		var elems []*node
		if p.peek().kind == tRBrack {
			return nil, fmt.Errorf("expr: empty list literal")
		}
		for {
			e, err := p.parseTern()
			if err != nil {
				return nil, err
			}
			elems = append(elems, e)
			if p.peek().kind != tComma {
				break
			}
			p.next()
		}
		if p.peek().kind != tRBrack {
			return nil, fmt.Errorf("expr: expected ']'")
		}
		p.next()
		return finish(&node{kind: nList}, elems...)
	default:
		return nil, fmt.Errorf("expr: unexpected %q", t.text)
	}
}

// ---- checked arithmetic (64-bit, fail closed) ----

const (
	maxInt64 = int64(^uint64(0) >> 1)
	minInt64 = -maxInt64 - 1
)

func checkedAdd(a, b int64) (int64, error) {
	s := a + b
	if (a^s)&(b^s) < 0 {
		return 0, fmt.Errorf("expr: integer overflow in addition")
	}
	return s, nil
}

func checkedSub(a, b int64) (int64, error) {
	s := a - b
	if (a^b)&(a^s) < 0 {
		return 0, fmt.Errorf("expr: integer overflow in subtraction")
	}
	return s, nil
}

func checkedNeg(a int64) (int64, error) {
	if a == minInt64 {
		return 0, fmt.Errorf("expr: integer overflow in negation")
	}
	return -a, nil
}

func checkedMul(a, b int64) (int64, error) {
	if a == minInt64 && b == -1 || b == minInt64 && a == -1 {
		return 0, fmt.Errorf("expr: integer overflow in multiplication")
	}
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/b != a {
		return 0, fmt.Errorf("expr: integer overflow in multiplication")
	}
	return r, nil
}

func checkedDiv(a, b int64) (int64, error) {
	if b == 0 {
		return 0, fmt.Errorf("expr: division by zero")
	}
	if a == minInt64 && b == -1 {
		return 0, fmt.Errorf("expr: integer overflow in division")
	}
	return a / b, nil
}

func checkedMod(a, b int64) (int64, error) {
	if b == 0 {
		return 0, fmt.Errorf("expr: modulo by zero")
	}
	if a == minInt64 && b == -1 {
		return 0, nil // Go defines MinInt64 % -1 == 0; no overflow possible.
	}
	return a % b, nil
}

// floorDiv is floored (round-toward-negative-infinity) division.
func floorDiv(a, b int64) (int64, error) {
	if b == 0 {
		return 0, fmt.Errorf("expr: division by zero in floor")
	}
	if a == minInt64 && b == -1 {
		return 0, fmt.Errorf("expr: integer overflow in floor")
	}
	q := a / b
	if (a^b) < 0 && a%b != 0 {
		q--
	}
	return q, nil
}

// ---- evaluation (pure, no I/O) ----

// EvalExpression parses and evaluates input against vars. Unknown variables
// and unknown functions are errors. Deterministic: same input+vars always
// yields the same output.
func EvalExpression(input string, vars map[string]Value) (Value, error) {
	n, err := ParseExpression(input)
	if err != nil {
		return Value{}, err
	}
	return n.eval(vars)
}

func (n *node) eval(vars map[string]Value) (Value, error) {
	switch n.kind {
	case nLit:
		return n.val, nil
	case nVar:
		v, ok := vars[n.name]
		if !ok {
			return Value{}, fmt.Errorf("expr: unknown variable %q", n.name)
		}
		return v, nil
	case nList:
		out := make([]int64, 0, len(n.kids))
		for _, k := range n.kids {
			v, err := k.eval(vars)
			if err != nil {
				return Value{}, err
			}
			iv, ok := v.Int()
			if !ok {
				return Value{}, fmt.Errorf("expr: list elements must be ints")
			}
			out = append(out, iv)
		}
		return ListValue(out), nil
	case nUnary:
		v, err := n.kids[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		switch n.op {
		case "-":
			iv, ok := v.Int()
			if !ok {
				return Value{}, fmt.Errorf("expr: unary '-' needs an int")
			}
			r, err := checkedNeg(iv)
			if err != nil {
				return Value{}, err
			}
			return IntValue(r), nil
		case "!":
			bv, ok := v.Bool()
			if !ok {
				return Value{}, fmt.Errorf("expr: unary '!' needs a bool")
			}
			return BoolValue(!bv), nil
		}
		return Value{}, fmt.Errorf("expr: unknown unary %q", n.op)
	case nBinary:
		return evalBinary(n.op, n.kids[0], n.kids[1], vars)
	case nTern:
		cv, err := n.kids[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		cb, ok := cv.Bool()
		if !ok {
			return Value{}, fmt.Errorf("expr: ternary condition must be bool")
		}
		if cb {
			return n.kids[1].eval(vars)
		}
		return n.kids[2].eval(vars)
	case nCall:
		return evalCall(n.name, n.kids, vars)
	}
	return Value{}, fmt.Errorf("expr: invalid node")
}

func evalBinary(op string, l, r *node, vars map[string]Value) (Value, error) {
	switch op {
	case "&&", "||":
		lv, err := l.eval(vars)
		if err != nil {
			return Value{}, err
		}
		lb, ok := lv.Bool()
		if !ok {
			return Value{}, fmt.Errorf("expr: %q needs bools", op)
		}
		// Short-circuit.
		if op == "&&" && !lb {
			return BoolValue(false), nil
		}
		if op == "||" && lb {
			return BoolValue(true), nil
		}
		rv, err := r.eval(vars)
		if err != nil {
			return Value{}, err
		}
		rb, ok := rv.Bool()
		if !ok {
			return Value{}, fmt.Errorf("expr: %q needs bools", op)
		}
		if op == "&&" {
			return BoolValue(rb), nil
		}
		return BoolValue(rb), nil
	}
	lv, err := l.eval(vars)
	if err != nil {
		return Value{}, err
	}
	rv, err := r.eval(vars)
	if err != nil {
		return Value{}, err
	}
	li, ok := lv.Int()
	if !ok {
		return Value{}, fmt.Errorf("expr: operator %q needs ints", op)
	}
	ri, ok := rv.Int()
	if !ok {
		return Value{}, fmt.Errorf("expr: operator %q needs ints", op)
	}
	switch op {
	case "+":
		s, err := checkedAdd(li, ri)
		if err != nil {
			return Value{}, err
		}
		return IntValue(s), nil
	case "-":
		s, err := checkedSub(li, ri)
		if err != nil {
			return Value{}, err
		}
		return IntValue(s), nil
	case "*":
		p, err := checkedMul(li, ri)
		if err != nil {
			return Value{}, err
		}
		return IntValue(p), nil
	case "/":
		q, err := checkedDiv(li, ri)
		if err != nil {
			return Value{}, err
		}
		return IntValue(q), nil
	case "%":
		m, err := checkedMod(li, ri)
		if err != nil {
			return Value{}, err
		}
		return IntValue(m), nil
	case "==":
		return BoolValue(li == ri), nil
	case "!=":
		return BoolValue(li != ri), nil
	case "<":
		return BoolValue(li < ri), nil
	case "<=":
		return BoolValue(li <= ri), nil
	case ">":
		return BoolValue(li > ri), nil
	case ">=":
		return BoolValue(li >= ri), nil
	}
	return Value{}, fmt.Errorf("expr: unknown operator %q", op)
}

func evalCall(name string, args []*node, vars map[string]Value) (Value, error) {
	ints := func() ([]int64, error) {
		out := make([]int64, 0, len(args))
		for _, a := range args {
			v, err := a.eval(vars)
			if err != nil {
				return nil, err
			}
			iv, ok := v.Int()
			if !ok {
				return nil, fmt.Errorf("expr: %q needs int args", name)
			}
			out = append(out, iv)
		}
		return out, nil
	}
	switch name {
	case "min", "max":
		vals, err := ints()
		if err != nil {
			return Value{}, err
		}
		if len(vals) == 0 {
			return Value{}, fmt.Errorf("expr: %q needs at least 1 arg", name)
		}
		best := vals[0]
		for _, v := range vals[1:] {
			if name == "min" && v < best || name == "max" && v > best {
				best = v
			}
		}
		return IntValue(best), nil
	case "abs":
		vals, err := ints()
		if err != nil {
			return Value{}, err
		}
		if len(vals) != 1 {
			return Value{}, fmt.Errorf("expr: abs needs exactly 1 arg")
		}
		if vals[0] == minInt64 {
			return Value{}, fmt.Errorf("expr: integer overflow in abs")
		}
		if vals[0] < 0 {
			return IntValue(-vals[0]), nil
		}
		return IntValue(vals[0]), nil
	case "floor":
		vals, err := ints()
		if err != nil {
			return Value{}, err
		}
		switch len(vals) {
		case 1:
			return IntValue(vals[0]), nil
		case 2:
			q, err := floorDiv(vals[0], vals[1])
			if err != nil {
				return Value{}, err
			}
			return IntValue(q), nil
		default:
			return Value{}, fmt.Errorf("expr: floor needs 1 or 2 args")
		}
	case "clamp":
		vals, err := ints()
		if err != nil {
			return Value{}, err
		}
		if len(vals) != 3 {
			return Value{}, fmt.Errorf("expr: clamp needs exactly 3 args (v, lo, hi)")
		}
		if vals[1] > vals[2] {
			return Value{}, fmt.Errorf("expr: clamp lo > hi")
		}
		if vals[0] < vals[1] {
			return IntValue(vals[1]), nil
		}
		if vals[0] > vals[2] {
			return IntValue(vals[2]), nil
		}
		return IntValue(vals[0]), nil
	case "sign":
		vals, err := ints()
		if err != nil {
			return Value{}, err
		}
		if len(vals) != 1 {
			return Value{}, fmt.Errorf("expr: sign needs exactly 1 arg")
		}
		switch {
		case vals[0] < 0:
			return IntValue(-1), nil
		case vals[0] > 0:
			return IntValue(1), nil
		default:
			return IntValue(0), nil
		}
	case "if":
		if len(args) != 3 {
			return Value{}, fmt.Errorf("expr: if needs exactly 3 args (cond, a, b)")
		}
		cv, err := args[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		cb, ok := cv.Bool()
		if !ok {
			return Value{}, fmt.Errorf("expr: if condition must be bool")
		}
		if cb {
			return args[1].eval(vars)
		}
		return args[2].eval(vars)
	case "kh", "kl":
		if len(args) != 2 {
			return Value{}, fmt.Errorf("expr: %q needs exactly 2 args (list, n)", name)
		}
		lv, err := args[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		ls, ok := lv.List()
		if !ok {
			return Value{}, fmt.Errorf("expr: %q first arg must be a list", name)
		}
		nv, err := args[1].eval(vars)
		if err != nil {
			return Value{}, err
		}
		n, ok := nv.Int()
		if !ok {
			return Value{}, fmt.Errorf("expr: %q second arg must be an int", name)
		}
		if n < 0 || n > int64(len(ls)) {
			return Value{}, fmt.Errorf("expr: %q n out of range (0..%d)", name, len(ls))
		}
		sorted := append([]int64(nil), ls...)
		if name == "kh" {
			sortDesc(sorted)
		} else {
			sortAsc(sorted)
		}
		return ListValue(sorted[:n]), nil
	case "sum":
		if len(args) != 1 {
			return Value{}, fmt.Errorf("expr: sum needs exactly 1 arg")
		}
		lv, err := args[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		ls, ok := lv.List()
		if !ok {
			return Value{}, fmt.Errorf("expr: sum needs a list arg")
		}
		var total int64
		for _, v := range ls {
			total, err = checkedAdd(total, v)
			if err != nil {
				return Value{}, err
			}
		}
		return IntValue(total), nil
	case "count":
		if len(args) != 1 {
			return Value{}, fmt.Errorf("expr: count needs exactly 1 arg")
		}
		lv, err := args[0].eval(vars)
		if err != nil {
			return Value{}, err
		}
		ls, ok := lv.List()
		if !ok {
			return Value{}, fmt.Errorf("expr: count needs a list arg")
		}
		return IntValue(int64(len(ls))), nil
	}
	// Unreachable: parser rejects unknown names. Kept as defense in depth so
	// a future AST constructor cannot silently ignore an unknown function.
	return Value{}, fmt.Errorf("expr: unknown function %q", name)
}

func sortDesc(s []int64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] > s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func sortAsc(s []int64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// VarsOf returns the variable names referenced by a parsed expression.
func (n *node) VarsOf() []string {
	seen := map[string]bool{}
	var walk func(m *node)
	walk = func(m *node) {
		if m.kind == nVar && !seen[m.name] {
			seen[m.name] = true
		}
		for _, k := range m.kids {
			walk(k)
		}
	}
	walk(n)
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	return out
}

// LintExpression validates an expression without evaluating it: parseable,
// known functions only, within depth/literal bounds. When knownVars is
// non-nil, referenced variables must be members (derived-stat formulas);
// when nil, variable checks are skipped (hook scripts see open context).
// It returns issues rather than failing silently on any input.
func LintExpression(expr string, knownVars map[string]bool) []LintIssue {
	n, err := ParseExpression(expr)
	if err != nil {
		return []LintIssue{{Severity: "error", Path: "expr", Message: err.Error()}}
	}
	if knownVars == nil {
		return nil
	}
	var issues []LintIssue
	for _, v := range n.VarsOf() {
		if !knownVars[v] {
			issues = append(issues, LintIssue{
				Severity: "error",
				Path:     "expr",
				Message:  fmt.Sprintf("unknown variable %q", v),
			})
		}
	}
	return issues
}

// ScopeFromContext flattens an intent context map into evaluator variables.
// Only ints (int/int64), integral float64s, and bools are visible; strings
// and other types are skipped (documented: the v1 engine has no strings).
// Non-integral floats are an error (fail closed, never silently truncate).
func ScopeFromContext(ctx map[string]any) (map[string]Value, error) {
	scope := make(map[string]Value, len(ctx))
	for k, v := range ctx {
		if !isValidIdent(k) {
			continue // context keys outside the ident grammar are invisible
		}
		switch t := v.(type) {
		case int:
			scope[k] = IntValue(int64(t))
		case int64:
			scope[k] = IntValue(t)
		case float64:
			if t != float64(int64(t)) {
				return nil, fmt.Errorf("expr: context key %q is a non-integral number", k)
			}
			scope[k] = IntValue(int64(t))
		case bool:
			scope[k] = BoolValue(t)
		default:
			continue // strings, lists, maps: not visible to v1 engine
		}
	}
	return scope, nil
}

func isValidIdent(s string) bool {
	if s == "" || s == "true" || s == "false" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
