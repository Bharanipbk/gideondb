// Package analysis turns record and query text into index terms.
//
// An Analyzer is a fixed pipeline: split into tokens (keeping byte offsets
// for highlighting), then lowercase, fold accents, drop stopwords and stem,
// as the analyzer's language requires. Collections pick one by name with
// CollectionConfig.TextAnalyzer; the zero value is "standard", which keeps
// the original tokenizer's behaviour exactly.
//
// Lexical indexes are rebuilt from record text when a segment loads, so an
// analyzer only has to be deterministic, never persisted.
package analysis

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Token is one index term and the byte range of the source text it came
// from. Start and End are offsets into the original string.
type Token struct {
	Term       string
	Start, End int
}

// Analyzer turns text into index terms.
type Analyzer struct {
	name      string
	split     func(string) []Token
	lowercase bool
	fold      bool
	stopwords map[string]struct{}
	stem      func(string) string
}

// Name reports the analyzer's registered name.
func (a *Analyzer) Name() string { return a.name }

// Analyze returns the analyzer's tokens in text order.
func (a *Analyzer) Analyze(text string) []Token {
	tokens := a.split(text)
	out := tokens[:0]
	for _, token := range tokens {
		term := token.Term
		if a.lowercase {
			term = strings.ToLower(term)
		}
		if a.fold {
			term = Fold(term)
		}
		if _, stop := a.stopwords[term]; stop {
			continue
		}
		if a.stem != nil {
			term = a.stem(term)
		}
		if term == "" {
			continue
		}
		token.Term = term
		out = append(out, token)
	}
	return out
}

// Terms returns only the terms of Analyze.
func (a *Analyzer) Terms(text string) []string {
	tokens := a.Analyze(text)
	terms := make([]string, len(tokens))
	for i, token := range tokens {
		terms[i] = token.Term
	}
	return terms
}

// DefaultName is the analyzer used when a collection names none.
const DefaultName = "standard"

var registry = map[string]*Analyzer{}

func register(a *Analyzer) { registry[a.name] = a }

// Get returns the named analyzer; "" selects DefaultName.
func Get(name string) (*Analyzer, error) {
	if name == "" {
		name = DefaultName
	}
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown text analyzer %q (have %s)", name, strings.Join(Names(), ", "))
	}
	return a, nil
}

// MustGet is Get for names already validated by CollectionConfig.Validate;
// an unknown name falls back to DefaultName so old data still loads.
func MustGet(name string) *Analyzer {
	if a, err := Get(name); err == nil {
		return a
	}
	return registry[DefaultName]
}

// Names lists registered analyzers in sorted order.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Fold removes combining marks after canonical decomposition ("café" →
// "cafe") and expands a few letters that do not decompose.
func Fold(term string) string {
	ascii := true
	for i := 0; i < len(term); i++ {
		if term[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return term
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(term) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		switch r {
		case 'ß':
			b.WriteString("ss")
		case 'æ':
			b.WriteString("ae")
		case 'œ':
			b.WriteString("oe")
		case 'ø':
			b.WriteRune('o')
		case 'ł':
			b.WriteRune('l')
		case 'đ':
			b.WriteRune('d')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// splitWords splits on every rune that is not a letter, digit or combining
// mark, keeping byte offsets.
func splitWords(text string) []Token {
	var tokens []Token
	start := -1
	for i, r := range text {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tokens = append(tokens, Token{Term: text[start:i], Start: start, End: i})
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, Token{Term: text[start:], Start: start, End: len(text)})
	}
	return tokens
}

// splitWhitespace splits on Unicode whitespace only.
func splitWhitespace(text string) []Token {
	var tokens []Token
	start := -1
	for i, r := range text {
		if !unicode.IsSpace(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			tokens = append(tokens, Token{Term: text[start:i], Start: start, End: i})
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, Token{Term: text[start:], Start: start, End: len(text)})
	}
	return tokens
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// splitCJK is splitWords, except that runs of Han, Kana and Hangul become
// overlapping bigrams (a lone character stays a unigram), as Lucene's CJK
// analyzer does; these scripts do not separate words with spaces.
func splitCJK(text string) []Token {
	var tokens []Token
	for _, word := range splitWords(text) {
		type position struct {
			offset int
			r      rune
		}
		var run []position
		flush := func() {
			switch len(run) {
			case 0:
			case 1:
				end := run[0].offset + utf8.RuneLen(run[0].r)
				tokens = append(tokens, Token{Term: text[run[0].offset:end], Start: run[0].offset, End: end})
			default:
				for i := 0; i+1 < len(run); i++ {
					end := run[i+1].offset + utf8.RuneLen(run[i+1].r)
					tokens = append(tokens, Token{Term: text[run[i].offset:end], Start: run[i].offset, End: end})
				}
			}
			run = run[:0]
		}
		otherStart := -1
		for i, r := range word.Term {
			offset := word.Start + i
			if isCJK(r) {
				if otherStart >= 0 {
					tokens = append(tokens, Token{Term: text[otherStart:offset], Start: otherStart, End: offset})
					otherStart = -1
				}
				run = append(run, position{offset, r})
				continue
			}
			flush()
			if otherStart < 0 {
				otherStart = offset
			}
		}
		flush()
		if otherStart >= 0 {
			tokens = append(tokens, Token{Term: text[otherStart:word.End], Start: otherStart, End: word.End})
		}
	}
	return tokens
}

func words(list string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, word := range strings.Fields(list) {
		set[word] = struct{}{}
	}
	return set
}

func init() {
	register(&Analyzer{name: "standard", split: splitWords, lowercase: true, stopwords: standardStopwords})
	register(&Analyzer{name: "simple", split: splitWords, lowercase: true})
	register(&Analyzer{name: "whitespace", split: splitWhitespace})
	register(&Analyzer{name: "folding", split: splitWords, lowercase: true, fold: true})
	register(&Analyzer{name: "cjk", split: splitCJK, lowercase: true, fold: true})
	register(&Analyzer{name: "english", split: splitWords, lowercase: true, fold: true, stopwords: englishStopwords, stem: porterStem})
	register(&Analyzer{name: "french", split: splitWords, lowercase: true, fold: true, stopwords: frenchStopwords, stem: frenchLightStem})
	register(&Analyzer{name: "german", split: splitWords, lowercase: true, fold: true, stopwords: germanStopwords, stem: germanLightStem})
	register(&Analyzer{name: "spanish", split: splitWords, lowercase: true, fold: true, stopwords: spanishStopwords, stem: spanishLightStem})
	register(&Analyzer{name: "italian", split: splitWords, lowercase: true, fold: true, stopwords: italianStopwords, stem: italianLightStem})
	register(&Analyzer{name: "portuguese", split: splitWords, lowercase: true, fold: true, stopwords: portugueseStopwords, stem: portugueseLightStem})
}

// standardStopwords is the original tokenizer's list, kept unchanged so
// "standard" collections score exactly as before.
var standardStopwords = words(`a an and are as at be by for from in is it of on or that the this to was with`)

// The language lists follow Lucene's default stopword sets; folded forms
// are used because folding runs first.
var (
	englishStopwords = words(`a an and are as at be but by for if in into is it no not of on or such that the their
		then there these they this to was will with`)
	frenchStopwords = words(`au aux avec ce ces dans de des du elle en et eux il je la le les leur lui ma mais me meme
		mes moi mon ne nos notre nous on ou par pas pour qu que qui sa se ses son sur ta te tes toi ton tu un une vos
		votre vous c d j l m n s t y ete etre a est sont`)
	germanStopwords = words(`aber als am an auch auf aus bei bin bis bist da dadurch daher darum das dass dein deine
		dem den der des dessen deshalb die dies dieser dieses doch dort du durch ein eine einem einen einer eines er es
		euer eure fur hatte hatten hattest hattet hier hinter ich ihr ihre im in ist ja jede jedem jeden jeder jedes
		jener jenes jetzt kann kannst konnen konnt machen mein meine mit muss musst musst nach nachdem nein nicht nun
		oder seid sein seine sich sie sind soll sollen sollst sollt sonst soweit sowie und unser unsere unter vom von
		vor wann warum was weiter weitere wenn wer werde werden werdet weshalb wie wieder wieso wir wird wirst wo woher
		wohin zu zum zur uber`)
	spanishStopwords = words(`de la que el en y a los del se las por un para con no una su al lo como mas pero sus le
		ya o este si porque esta entre cuando muy sin sobre tambien me hasta hay donde quien desde todo nos durante
		todos uno les ni contra otros ese eso ante ellos e esto mi antes algunos que unos yo otro otras otra el tanto
		esa estos mucho quienes nada muchos cual poco ella estar estas algunas algo nosotros es son fue`)
	italianStopwords = words(`ad al allo ai agli all agl alla alle con col coi da dal dallo dai dagli dall dagl dalla
		dalle di del dello dei degli dell degl della delle in nel nello nei negli nell negl nella nelle su sul sullo sui
		sugli sull sugl sulla sulle per tra contro io tu lui lei noi voi loro mio mia miei mie tuo tua tuoi tue suo sua
		suoi sue nostro nostra nostri nostre vostro vostra vostri vostre mi ti ci vi lo la li le gli ne il un uno una ma
		ed se perche anche come dov dove che chi cui non piu quale quanto quanti quanta quante quello quelli quella
		quelle questo questi questa queste si tutto tutti a c e i l o ho hai ha e sono`)
	portugueseStopwords = words(`de a o que e do da em um para com nao uma os no se na por mais as dos como mas ao ele
		das a seu sua ou quando muito nos ja eu tambem so pelo pela ate isso ela entre depois sem mesmo aos seus quem
		nas me esse eles voce essa num nem suas meu minha numa pelos elas qual nos lhe deles essas esses pelas este
		dele tu te voces vos lhes meus minhas teu tua teus tuas nosso nossa nossos nossas dela delas esta estes estas
		aquele aquela aqueles aquelas isto aquilo e sao foi`)
)
