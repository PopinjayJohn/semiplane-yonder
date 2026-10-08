package dice

import (
	"testing"
)

func TestParseNotationValid(t *testing.T) {
	symbols := SymbolTable{"F": {"plus", "blank", "minus"}}
	cases := []struct {
		notation string
		check    func(*testing.T, *Spec)
	}{
		{"d20", func(t *testing.T, s *Spec) {
			if s.Count != 1 || s.Faces != 20 {
				t.Errorf("d20 = %+v", s)
			}
		}},
		{"2d6+3", func(t *testing.T, s *Spec) {
			if s.Count != 2 || s.Faces != 6 || s.Modifier != 3 {
				t.Errorf("2d6+3 = %+v", s)
			}
		}},
		{"2d6+3-1", func(t *testing.T, s *Spec) {
			if s.Modifier != 2 {
				t.Errorf("2d6+3-1 modifier = %d, want 2", s.Modifier)
			}
		}},
		{"4d6kh3", func(t *testing.T, s *Spec) {
			if s.Keep != KeepHigh || s.KeepN != 3 {
				t.Errorf("4d6kh3 keep = %v/%d", s.Keep, s.KeepN)
			}
		}},
		{"4d6kl1", func(t *testing.T, s *Spec) {
			if s.Keep != KeepLow || s.KeepN != 1 {
				t.Errorf("4d6kl1 keep = %v/%d", s.Keep, s.KeepN)
			}
		}},
		{"4d6dl1", func(t *testing.T, s *Spec) {
			// Drop lowest 1 of 4 = keep highest 3.
			if s.Keep != KeepHigh || s.KeepN != 3 {
				t.Errorf("4d6dl1 = keep %v/%d, want high/3", s.Keep, s.KeepN)
			}
		}},
		{"8d6!", func(t *testing.T, s *Spec) {
			if !s.Explode {
				t.Errorf("8d6! explode not set: %+v", s)
			}
		}},
		{"4d6r1", func(t *testing.T, s *Spec) {
			if s.RerollLT != 1 {
				t.Errorf("4d6r1 reroll = %d, want 1", s.RerollLT)
			}
		}},
		{"1d20adv", func(t *testing.T, s *Spec) {
			if s.Adv != 1 {
				t.Errorf("1d20adv = %d, want 1", s.Adv)
			}
		}},
		{"1d20dis", func(t *testing.T, s *Spec) {
			if s.Adv != -1 {
				t.Errorf("1d20dis = %d, want -1", s.Adv)
			}
		}},
		{" 2d6 + 3 ", func(t *testing.T, s *Spec) {
			if s.Count != 2 || s.Modifier != 3 {
				t.Errorf("whitespace notation = %+v", s)
			}
		}},
		{"3dF", func(t *testing.T, s *Spec) {
			if !s.IsSymbolic() || len(s.Symbols) != 3 {
				t.Errorf("3dF symbols = %v", s.Symbols)
			}
		}},
	}
	for _, c := range cases {
		spec, err := ParseNotation(c.notation, symbols)
		if err != nil {
			t.Errorf("ParseNotation(%q) error: %v", c.notation, err)
			continue
		}
		c.check(t, spec)
	}
}

func TestParseNotationErrors(t *testing.T) {
	symbols := SymbolTable{"F": {"plus", "blank", "minus"}}
	cases := []string{
		"", "abc", "d", "2d", "0d6", "101d6", // empty / malformed / out of bounds
		"2d1001",               // faces exceed absolute max
		"2dX",                  // unknown die without table
		"2dF",                  // symbol die, nil table
		"4d6kh5",               // keep more than rolled
		"4d6kh3kh2",            // duplicate keep
		"8d6!!",                // duplicate explode
		"4d6r1r2",              // duplicate reroll
		"2d6+",                 // dangling sign
		"2d6advdis",            // trailing garbage
		"1d20foo",              // unknown suffix
		"999999999999999999d6", // number exceeds max
	}
	for _, n := range cases {
		if _, err := ParseNotation(n, nil); err == nil {
			t.Errorf("ParseNotation(%q) succeeded, want error", n)
		}
	}
	// Unknown symbol with a table present still fails.
	if _, err := ParseNotation("2dF", nil); err == nil {
		t.Error("ParseNotation(2dF, nil) succeeded, want error")
	}
	if _, err := ParseNotation("2dQ", symbols); err == nil {
		t.Error("ParseNotation(2dQ) succeeded, want error")
	}
}

func TestValidateNotation(t *testing.T) {
	if err := ValidateNotation("2d6+3"); err != nil {
		t.Errorf("ValidateNotation(2d6+3) = %v", err)
	}
	if err := ValidateNotation("bogus"); err == nil {
		t.Error("ValidateNotation(bogus) succeeded, want error")
	}
}

func TestSpecWithinConfig(t *testing.T) {
	spec, err := ParseNotation("2d6", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Bases may set lower maxima within the absolute transport maxima.
	if err := spec.WithinConfig(2, 6); err != nil {
		t.Errorf("WithinConfig(2,6) = %v, want nil", err)
	}
	if err := spec.WithinConfig(1, 6); err == nil {
		t.Error("WithinConfig(1,6) succeeded, want error (base max 1 die)")
	}
	if err := spec.WithinConfig(2, 4); err == nil {
		t.Error("WithinConfig(2,4) succeeded, want error (base max d4)")
	}
}
