package analysis

import (
	"reflect"
	"testing"
)

func TestPorterStemKnownPairs(t *testing.T) {
	// Pairs from Porter's published vocabulary/output lists.
	cases := map[string]string{
		"caresses": "caress", "ponies": "poni", "ties": "ti", "caress": "caress", "cats": "cat",
		"feed": "feed", "agreed": "agre", "plastered": "plaster", "bled": "bled", "motoring": "motor",
		"sing": "sing", "conflated": "conflat", "troubled": "troubl", "sized": "size", "hopping": "hop",
		"tanned": "tan", "falling": "fall", "hissing": "hiss", "fizzed": "fizz", "failing": "fail",
		"filing": "file", "happy": "happi", "sky": "sky", "relational": "relat", "conditional": "condit",
		"rational": "ration", "valenci": "valenc", "digitizer": "digit", "conformabli": "conform",
		"radicalli": "radic", "differentli": "differ", "vileli": "vile", "analogousli": "analog",
		"vietnamization": "vietnam", "predication": "predic", "operator": "oper", "feudalism": "feudal",
		"decisiveness": "decis", "hopefulness": "hope", "callousness": "callous", "formaliti": "formal",
		"sensitiviti": "sensit", "sensibiliti": "sensibl", "triplicate": "triplic", "formative": "form",
		"formalize": "formal", "electriciti": "electr", "electrical": "electr", "hopeful": "hope",
		"goodness": "good", "revival": "reviv", "allowance": "allow", "inference": "infer",
		"airliner": "airlin", "gyroscopic": "gyroscop", "adjustable": "adjust", "defensible": "defens",
		"irritant": "irrit", "replacement": "replac", "adjustment": "adjust", "dependent": "depend",
		"adoption": "adopt", "homologou": "homolog", "communism": "commun", "activate": "activ",
		"angulariti": "angular", "homologous": "homolog", "effective": "effect", "bowdlerize": "bowdler",
		"probate": "probat", "rate": "rate", "cease": "ceas", "controll": "control", "roll": "roll",
		"generalizations": "gener", "running": "run", "searches": "search",
	}
	for word, want := range cases {
		if got := porterStem(word); got != want {
			t.Errorf("porterStem(%q) = %q, want %q", word, got, want)
		}
	}
}

func TestStandardMatchesOriginalTokenizer(t *testing.T) {
	a := MustGet("")
	got := a.Terms("The Quick-Brown fox, IS jumping over 2 lazy dogs!")
	want := []string{"quick", "brown", "fox", "jumping", "over", "2", "lazy", "dogs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("standard terms = %v, want %v", got, want)
	}
}

func TestOffsetsPointAtSourceText(t *testing.T) {
	text := "Crème brûlée, s'il vous plaît"
	for _, name := range Names() {
		for _, token := range MustGet(name).Analyze(text) {
			if token.Start < 0 || token.End > len(text) || token.Start >= token.End {
				t.Fatalf("%s: bad offsets %+v", name, token)
			}
		}
	}
	tokens := MustGet("french").Analyze(text)
	if len(tokens) == 0 || text[tokens[0].Start:tokens[0].End] != "Crème" {
		t.Fatalf("french first token = %+v", tokens)
	}
}

func TestLanguageAnalyzersConflateInflections(t *testing.T) {
	cases := []struct {
		analyzer string
		a, b     string
	}{
		{"english", "connections", "connected"},
		{"english", "Running", "runs"},
		{"french", "chevaux", "cheval"},
		{"french", "maisons", "maison"},
		{"german", "Häuser", "hauser"},
		{"german", "Kindern", "kinder"},
		{"spanish", "canciones", "cancion"},
		{"spanish", "luces", "luz"},
		{"italian", "libri", "libro"},
		{"portuguese", "canções", "canção"},
		{"portuguese", "animais", "animal"},
		{"folding", "café", "CAFE"},
	}
	for _, c := range cases {
		a := MustGet(c.analyzer)
		if x, y := a.Terms(c.a), a.Terms(c.b); !reflect.DeepEqual(x, y) {
			t.Errorf("%s: %q → %v but %q → %v", c.analyzer, c.a, x, c.b, y)
		}
	}
}

func TestCJKBigrams(t *testing.T) {
	got := MustGet("cjk").Terms("东京大学 and Tokyo")
	want := []string{"东京", "京大", "大学", "and", "tokyo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cjk terms = %v, want %v", got, want)
	}
	if got := MustGet("cjk").Terms("中"); !reflect.DeepEqual(got, []string{"中"}) {
		t.Fatalf("single CJK rune = %v", got)
	}
}

func TestGetRejectsUnknown(t *testing.T) {
	if _, err := Get("klingon"); err == nil {
		t.Fatal("unknown analyzer accepted")
	}
	if MustGet("klingon").Name() != DefaultName {
		t.Fatal("MustGet must fall back to the default")
	}
}
