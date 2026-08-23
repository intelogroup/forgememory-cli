package tokens

import (
	"reflect"
	"testing"
)

func TestTokenSet_Basic(t *testing.T) {
	set := TokenSet("Hello world hello", nil)
	if !set["hello"] || !set["world"] {
		t.Fatalf("expected hello/world in %v", set)
	}
	if len(set) != 2 {
		t.Fatalf("dedup failed %v", set)
	}
}

func TestTokenSet_StopWords(t *testing.T) {
	set := TokenSet("the quick brown fox and the agent", CommonStopWords)
	if set["the"] || set["and"] || set["agent"] {
		t.Fatalf("stopwords not filtered %v", set)
	}
	if !set["quick"] || !set["brown"] {
		t.Fatalf("expected quick/brown %v", set)
	}
}

func TestTokenSet_Short(t *testing.T) {
	set := TokenSet("a an go hi work", CommonStopWords)
	if set["a"] || set["an"] || set["go"] || set["hi"] {
		t.Fatalf("short tokens not dropped %v", set)
	}
	if set["work"] {
		t.Fatalf("work is stopword, should be filtered %v", set)
	}
	if len(set) != 0 {
		t.Fatalf("expected empty set, got %v", set)
	}
}

func TestTokenize_CappedSorted(t *testing.T) {
	// tokens <3 chars (mu, nu, xi) dropped, so 11 remain, not capped
	text := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi"
	got := Tokenize(text, nil)
	if len(got) != 11 {
		t.Fatalf("expected 11, got %d %v", len(got), got)
	}
	want := []string{"alpha", "beta", "delta", "epsilon", "eta", "gamma", "iota", "kappa", "lambda", "theta", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v got %v", want, got)
	}
	// now test cap at 12 with 14 valid >=3 tokens
	text2 := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda omicron pi sigma tau"
	// pi (2) dropped, so 13 valid -> capped to 12
	got2 := Tokenize(text2, nil)
	if len(got2) != 12 {
		t.Fatalf("expected cap 12, got %d %v", len(got2), got2)
	}
}

func TestTokenize_WithStopWords(t *testing.T) {
	got := Tokenize("the agent wants to build the feature", CommonStopWords)
	// "the","agent" etc filtered, only "feature" may remain? check "build" also stopword
	for _, tok := range got {
		if CommonStopWords[tok] {
			t.Fatalf("stopword leaked %q in %v", tok, got)
		}
	}
}
