package ruleset

import (
	"math/big"
	mrand "math/rand"
	"strings"
	"testing"
)

func newTestRNG(seed int64) *mrand.Rand {
	return mrand.New(mrand.NewSource(seed))
}

func mustEval(t *testing.T, expr string, vars map[string]Value) Value {
	t.Helper()
	v, err := EvalExpression(expr, vars)
	if err != nil {
		t.Fatalf("EvalExpression(%q) error: %v", expr, err)
	}
	return v
}

func mustInt(t *testing.T, expr string, vars map[string]Value) int64 {
	t.Helper()
	v := mustEval(t, expr, vars)
	i, ok := v.Int()
	if !ok {
		t.Fatalf("EvalExpression(%q) = %v, want int", expr, v)
	}
	return i
}

func mustBool(t *testing.T, expr string, vars map[string]Value) bool {
	t.Helper()
	v := mustEval(t, expr, vars)
	b, ok := v.Bool()
	if !ok {
		t.Fatalf("EvalExpression(%q) = %v, want bool", expr, v)
	}
	return b
}

func TestArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want int64
	}{
		{"1+2*3", 7},
		{"(1+2)*3", 9},
		{"10-4-3", 3}, // left assoc
		{"20/4/2", 2},
		{"7%3", 1},
		{"-5+2", -3},
		{"--5", 5},
		{"2*3+4*5", 26},
		{"10/3", 3},   // truncated
		{"-10/3", -3}, // truncated toward zero
		{"-10%3", -1},
		{"floor(7,2)", 3},
		{"floor(-7,2)", -4}, // floored, unlike /
		{"floor(5)", 5},
		{"min(3,1,2)", 1},
		{"max(3,1,2)", 3},
		{"min(5)", 5},
		{"abs(-4)", 4},
		{"abs(4)", 4},
		{"clamp(10,1,5)", 5},
		{"clamp(-2,1,5)", 1},
		{"clamp(3,1,5)", 3},
		{"sign(-9)", -1},
		{"sign(0)", 0},
		{"sign(9)", 1},
		{"if(1<2,10,20)", 10},
		{"if(1>2,10,20)", 20},
		{"1<2 ? 10 : 20", 10},
		{"1>2 ? 10 : 20", 20},
		{"sum([1,2,3])", 6},
		{"count([1,2,3])", 3},
		{"sum(kh([4,1,6,2],2))", 10},
		{"sum(kl([4,1,6,2],2))", 3},
	}
	for _, c := range cases {
		if got := mustInt(t, c.expr, nil); got != c.want {
			t.Errorf("EvalExpression(%q) = %d, want %d", c.expr, got, c.want)
		}
	}
}

func TestBoolean(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"1<2", true}, {"2<1", false},
		{"2<=2", true}, {"3>=4", false},
		{"1==1", true}, {"1!=1", false},
		{"true && false", false}, {"true || false", true},
		{"!true", false}, {"!(1==2)", true},
		{"1<2 && 3<4 || false", true},
	}
	for _, c := range cases {
		if got := mustBool(t, c.expr, nil); got != c.want {
			t.Errorf("EvalExpression(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
	// Short-circuit: RHS error must not fire when LHS decides.
	if got := mustBool(t, "false && (1/0==0)", nil); got {
		t.Error("short-circuit && failed")
	}
	if got := mustBool(t, "true || (1/0==0)", nil); !got {
		t.Error("short-circuit || failed")
	}
}

func TestVariables(t *testing.T) {
	vars := map[string]Value{
		"str": IntValue(16), "dex": IntValue(14), "flanking": BoolValue(true),
	}
	if got := mustInt(t, "floor((str-10),2)", vars); got != 3 {
		t.Errorf("ability modifier = %d, want 3", got)
	}
	if got := mustBool(t, "flanking && str>10", vars); !got {
		t.Error("flanking check failed")
	}
}

func TestErrors(t *testing.T) {
	cases := []string{
		"1/0", "1%0", "floor(1,0)",
		"9223372036854775807+1",       // overflow add
		"-9223372036854775808-1",      // overflow sub
		"3037000500*3037000500",       // overflow mul
		"(-9223372036854775807-1)/-1", // MinInt64/-1
		"(-9223372036854775807-1)%-1", // MinInt64%-1 is defined as 0 (Go semantics), handled below
		"min()", "abs(1,2)", "floor(1,2,3)", "clamp(1,2)", "clamp(1,5,2)",
		"sign()", "if(true,1)", "sum(1)", "count(1)", "kh(1,2)", "kl([1],-1)",
		"sum(kh([4,1],5))", "count([])", "[]",
		"bogusfn(1)", // unknown function fails, never ignored
		"nope+1",     // unknown variable fails
		"1+true",     // type error
		"true+1",     // type error
		"!5",         // type error
		"-true",      // type error
		"1 && true",  // type error
		"1 ? 2 : 3",  // non-bool condition
		"[1,true]",   // non-int list element
		"\"str\"",    // no strings in v1
		"1==true",    // cross-type comparison
		"(1+2", "1+2)", "1 2", "2d6", "=",
	}
	for _, expr := range cases {
		if expr == "(-9223372036854775807-1)%-1" {
			// Defined: Go semantics give 0; our engine returns 0, no error.
			if got := mustInt(t, expr, nil); got != 0 {
				t.Errorf("MinInt64%%-1 = %d, want 0", got)
			}
			continue
		}
		if _, err := EvalExpression(expr, nil); err == nil {
			t.Errorf("EvalExpression(%q) succeeded, want error", expr)
		}
	}
	// abs(MinInt64) and negation overflow.
	if _, err := EvalExpression("abs(-9223372036854775807-1)", nil); err == nil {
		t.Error("abs(MinInt64) succeeded, want error")
	}
	if _, err := EvalExpression("-(-9223372036854775807-1)", nil); err == nil {
		t.Error("neg(MinInt64) succeeded, want error")
	}
	// Unknown function error names the function (lint surfaces it too).
	if _, err := EvalExpression("fly(speed)", nil); err == nil ||
		!strings.Contains(err.Error(), `"fly"`) {
		t.Errorf("unknown function error = %v, want name quoted", err)
	}
}

func TestBounds(t *testing.T) {
	// Depth: 33 nested parens... note parens splice through without adding
	// depth, so build depth with unary/binary nesting instead.
	deep := "1"
	for i := 0; i < MaxExprDepth+2; i++ {
		deep = "-(" + deep + ")"
	}
	if _, err := EvalExpression(deep, nil); err == nil {
		t.Error("over-deep expression succeeded, want error")
	}
	shallow := "1"
	for i := 0; i < MaxExprDepth-2; i++ {
		shallow = "-(" + shallow + ")"
	}
	if got := mustInt(t, shallow, nil); got != 1 && got != -1 {
		t.Errorf("boundary-depth expr = %d", got)
	}
	// Literals: 257 ones summed.
	var sb strings.Builder
	for i := 0; i < MaxExprLiterals+1; i++ {
		if i > 0 {
			sb.WriteByte('+')
		}
		sb.WriteByte('1')
	}
	if _, err := EvalExpression(sb.String(), nil); err == nil {
		t.Error("over-literal expression succeeded, want error")
	}
	// Length cap.
	if _, err := EvalExpression(strings.Repeat("1+", MaxExprLen), nil); err == nil {
		t.Error("over-long expression succeeded, want error")
	}
}

func TestLintExpression(t *testing.T) {
	if issues := LintExpression("bogusfn(1)", nil); len(issues) == 0 {
		t.Error("lint missed unknown function")
	}
	if issues := LintExpression("1+", nil); len(issues) == 0 {
		t.Error("lint missed syntax error")
	}
	known := map[string]bool{"str": true, "dex": true}
	if issues := LintExpression("str+dex", known); len(issues) != 0 {
		t.Errorf("lint flagged valid formula: %v", issues)
	}
	if issues := LintExpression("str+nope", known); len(issues) == 0 {
		t.Error("lint missed unknown variable with known set")
	}
	// Nil set skips variable checks (hook scripts see open context).
	if issues := LintExpression("flanking && str>10", nil); len(issues) != 0 {
		t.Errorf("lint flagged open-context hook script: %v", issues)
	}
}

func TestScopeFromContext(t *testing.T) {
	_, err := ScopeFromContext(map[string]any{
		"n": 3, "big": int64(9), "f": 4.0, "b": true,
		"s": "longsword", "bad.key": 1, "frac": 1.5,
	})
	if err == nil {
		t.Fatal("non-integral float did not fail")
	}
	scope, err := ScopeFromContext(map[string]any{
		"n": 3, "big": int64(9), "f": 4.0, "b": true,
		"s": "longsword", "bad.key": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 4 {
		t.Fatalf("scope has %d vars, want 4 (strings/dotted keys skipped)", len(scope))
	}
	if got := mustInt(t, "n+big+f", scope); got != 16 {
		t.Errorf("scope math = %d, want 16", got)
	}
}

// Property: checked addition either matches big.Int or fails (never wraps).
func TestPropertyAddNeverWraps(t *testing.T) {
	rng := newTestRNG(12345)
	for i := 0; i < 2000; i++ {
		a := rng.Int63() - rng.Int63() // explore negatives heavily
		b := rng.Int63() - rng.Int63()
		vars := map[string]Value{"a": IntValue(a), "b": IntValue(b)}
		got, err := EvalExpression("a+b", vars)
		ref := new(big.Int).Add(big.NewInt(a), big.NewInt(b))
		if ref.IsInt64() {
			if err != nil {
				t.Fatalf("a+b=%d+%d errored but fits: %v", a, b, err)
			}
			if v, _ := got.Int(); v != ref.Int64() {
				t.Fatalf("a+b=%d+%d = %d, want %d", a, b, v, ref.Int64())
			}
		} else if err == nil {
			t.Fatalf("a+b=%d+%d wrapped instead of failing", a, b)
		}
	}
}

// Property: kh/kl return the right count, sorted, drawn from the input.
func TestPropertyKeepHighestLowest(t *testing.T) {
	rng := newTestRNG(999)
	for i := 0; i < 500; i++ {
		n := 1 + rng.Intn(8)
		ls := make([]int64, n)
		for j := range ls {
			ls[j] = int64(rng.Intn(21))
		}
		k := rng.Intn(n + 1)
		vars := map[string]Value{"xs": ListValue(ls)}
		high := i%2 == 0
		expr := stringOf(map[bool]string{true: "kh(xs,", false: "kl(xs,"}[high], k, ")")
		v, err := EvalExpression(expr, vars)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := v.List()
		if len(got) != k {
			t.Fatalf("%s on %v kept %d, want %d", expr, ls, len(got), k)
		}
		counts := map[int64]int{}
		for _, x := range ls {
			counts[x]++
		}
		for j, x := range got {
			counts[x]--
			if counts[x] < 0 {
				t.Fatalf("%s on %v invented %d", expr, ls, x)
			}
			if j > 0 {
				if high && x > got[j-1] {
					t.Fatalf("kh unsorted: %v", got)
				}
				if !high && x < got[j-1] {
					t.Fatalf("kl unsorted: %v", got)
				}
			}
		}
		// Extremeness: kept sum is maximal (kh) or minimal (kl) — verify by
		// brute force on these tiny inputs via sorted reference.
		ref := append([]int64(nil), ls...)
		if high {
			sortDesc(ref)
		} else {
			sortAsc(ref)
		}
		var want, have int64
		for _, x := range ref[:k] {
			want += x
		}
		for _, x := range got {
			have += x
		}
		if want != have {
			t.Fatalf("%s on %v kept sum %d, want %d", expr, ls, have, want)
		}
	}
}

func stringOf(parts ...any) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(intOrString(p))
	}
	return sb.String()
}

func intOrString(p any) string {
	if i, ok := p.(int); ok {
		return big.NewInt(int64(i)).String()
	}
	return p.(string)
}
