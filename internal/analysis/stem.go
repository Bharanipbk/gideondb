package analysis

import "strings"

// porterStem implements the original Porter (1980) stemming algorithm for
// lowercase English words. Words of two letters or fewer, and words that
// are not plain ASCII letters, are returned unchanged.
func porterStem(word string) string {
	if len(word) <= 2 {
		return word
	}
	for i := 0; i < len(word); i++ {
		if word[i] < 'a' || word[i] > 'z' {
			return word
		}
	}
	p := &porter{b: []byte(word)}
	p.step1ab()
	p.step1c()
	p.step2()
	p.step3()
	p.step4()
	p.step5()
	return string(p.b)
}

type porter struct {
	b []byte
	j int // end of the stem being considered by ends
}

func (p *porter) cons(i int) bool {
	switch p.b[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		return i == 0 || !p.cons(i-1)
	}
	return true
}

// m counts VC sequences in b[0..j].
func (p *porter) m() int {
	n, i := 0, 0
	for {
		if i > p.j {
			return n
		}
		if !p.cons(i) {
			break
		}
		i++
	}
	i++
	for {
		for {
			if i > p.j {
				return n
			}
			if p.cons(i) {
				break
			}
			i++
		}
		i++
		n++
		for {
			if i > p.j {
				return n
			}
			if !p.cons(i) {
				break
			}
			i++
		}
		i++
	}
}

func (p *porter) vowelInStem() bool {
	for i := 0; i <= p.j; i++ {
		if !p.cons(i) {
			return true
		}
	}
	return false
}

func (p *porter) doubleC(i int) bool {
	return i >= 1 && p.b[i] == p.b[i-1] && p.cons(i)
}

// cvc reports consonant-vowel-consonant ending at i, where the last
// consonant is not w, x or y.
func (p *porter) cvc(i int) bool {
	if i < 2 || !p.cons(i) || p.cons(i-1) || !p.cons(i-2) {
		return false
	}
	switch p.b[i] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

func (p *porter) ends(s string) bool {
	if len(s) > len(p.b) || string(p.b[len(p.b)-len(s):]) != s {
		return false
	}
	p.j = len(p.b) - len(s) - 1
	return true
}

func (p *porter) setTo(s string) { p.b = append(p.b[:p.j+1], s...) }

func (p *porter) r(s string) {
	if p.m() > 0 {
		p.setTo(s)
	}
}

func (p *porter) step1ab() {
	if p.b[len(p.b)-1] == 's' {
		switch {
		case p.ends("sses"):
			p.b = p.b[:len(p.b)-2]
		case p.ends("ies"):
			p.setTo("i")
		case len(p.b) >= 2 && p.b[len(p.b)-2] != 's':
			p.b = p.b[:len(p.b)-1]
		}
	}
	if p.ends("eed") {
		if p.m() > 0 {
			p.b = p.b[:len(p.b)-1]
		}
		return
	}
	if (p.ends("ed") || p.ends("ing")) && p.vowelInStem() {
		p.b = p.b[:p.j+1]
		k := len(p.b) - 1
		switch {
		case p.ends("at"):
			p.setTo("ate")
		case p.ends("bl"):
			p.setTo("ble")
		case p.ends("iz"):
			p.setTo("ize")
		case p.doubleC(k):
			switch p.b[k] {
			case 'l', 's', 'z':
			default:
				p.b = p.b[:k]
			}
		default:
			p.j = k
			if p.m() == 1 && p.cvc(k) {
				p.b = append(p.b, 'e')
			}
		}
	}
}

func (p *porter) step1c() {
	if p.ends("y") && p.vowelInStem() {
		p.b[len(p.b)-1] = 'i'
	}
}

var porterStep2 = [][2]string{
	{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"}, {"anci", "ance"}, {"izer", "ize"},
	{"bli", "ble"}, {"alli", "al"}, {"entli", "ent"}, {"eli", "e"}, {"ousli", "ous"},
	{"ization", "ize"}, {"ation", "ate"}, {"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"},
	{"fulness", "ful"}, {"ousness", "ous"}, {"aliti", "al"}, {"iviti", "ive"}, {"biliti", "ble"},
	{"logi", "log"},
}

func (p *porter) step2() {
	if len(p.b) < 2 {
		return
	}
	for _, rule := range porterStep2 {
		if p.ends(rule[0]) {
			p.r(rule[1])
			return
		}
	}
}

var porterStep3 = [][2]string{
	{"icate", "ic"}, {"ative", ""}, {"alize", "al"}, {"iciti", "ic"}, {"ical", "ic"}, {"ful", ""}, {"ness", ""},
}

func (p *porter) step3() {
	for _, rule := range porterStep3 {
		if p.ends(rule[0]) {
			p.r(rule[1])
			return
		}
	}
}

var porterStep4 = []string{
	"al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement", "ment", "ent",
	"ion", "ou", "ism", "ate", "iti", "ous", "ive", "ize",
}

func (p *porter) step4() {
	for _, suffix := range porterStep4 {
		if !p.ends(suffix) {
			continue
		}
		if suffix == "ion" && (p.j < 0 || (p.b[p.j] != 's' && p.b[p.j] != 't')) {
			return
		}
		if p.m() > 1 {
			p.b = p.b[:p.j+1]
		}
		return
	}
}

func (p *porter) step5() {
	p.j = len(p.b) - 1
	if p.b[len(p.b)-1] == 'e' {
		p.j = len(p.b) - 2
		if a := p.m(); a > 1 || (a == 1 && !p.cvc(len(p.b)-2)) {
			p.b = p.b[:len(p.b)-1]
		}
	}
	p.j = len(p.b) - 1
	if p.b[len(p.b)-1] == 'l' && p.doubleC(len(p.b)-1) && p.m() > 1 {
		p.b = p.b[:len(p.b)-1]
	}
}

// The light stemmers below follow Jacques Savoy's light stemmers (as in
// Lucene's *LightStemmer classes): they strip inflection, mostly plural and
// gender endings, and leave derivation alone. They run after folding, so
// they see unaccented letters.

func stripFirst(word string, minStem int, suffixes ...string) (string, bool) {
	for _, suffix := range suffixes {
		if len(word)-len(suffix) >= minStem && strings.HasSuffix(word, suffix) {
			return word[:len(word)-len(suffix)], true
		}
	}
	return word, false
}

func frenchLightStem(word string) string {
	if len(word) < 5 {
		return word
	}
	if w, ok := stripFirst(word, 3, "aux"); ok {
		word = w + "al"
	} else if w, ok := stripFirst(word, 3, "s", "x"); ok {
		word = w
	}
	word, _ = stripFirst(word, 4, "issement", "ement", "ation", "euse", "eux", "ique", "able", "ance", "ence", "ite")
	word, _ = stripFirst(word, 3, "ee", "er", "ez", "e")
	if n := len(word); n > 3 && word[n-1] == word[n-2] && strings.IndexByte("bcdfghjklmnpqrstvwxz", word[n-1]) >= 0 {
		word = word[:n-1]
	}
	return word
}

func germanLightStem(word string) string {
	if len(word) < 4 {
		return word
	}
	if w, ok := stripFirst(word, 3, "ern"); ok {
		return w
	}
	if w, ok := stripFirst(word, 3, "em", "en", "er", "es"); ok {
		return w
	}
	if w, ok := stripFirst(word, 3, "e"); ok {
		return w
	}
	if n := len(word); n > 4 && word[n-1] == 's' && strings.IndexByte("bdfghklmnrt", word[n-2]) >= 0 {
		return word[:n-1]
	}
	if w, ok := stripFirst(word, 3, "n"); ok {
		return w
	}
	return word
}

func spanishLightStem(word string) string {
	if len(word) < 5 {
		return word
	}
	if w, ok := stripFirst(word, 3, "eses"); ok {
		return w + "es"
	}
	if w, ok := stripFirst(word, 2, "ces"); ok {
		return w + "z"
	}
	if w, ok := stripFirst(word, 3, "os", "as", "es"); ok {
		return w
	}
	if w, ok := stripFirst(word, 3, "o", "a", "e"); ok {
		return w
	}
	return word
}

func italianLightStem(word string) string {
	if len(word) < 5 {
		return word
	}
	if w, ok := stripFirst(word, 3, "chi", "ghi", "che", "ghe"); ok {
		return w + word[len(w):len(w)+2]
	}
	if w, ok := stripFirst(word, 3, "ii", "ie", "io", "ia"); ok {
		return w + "i"
	}
	if w, ok := stripFirst(word, 3, "a", "e", "i", "o"); ok {
		return w
	}
	return word
}

func portugueseLightStem(word string) string {
	if len(word) < 4 {
		return word
	}
	switch {
	case strings.HasSuffix(word, "oes") || strings.HasSuffix(word, "aes"):
		word = word[:len(word)-3] + "ao"
	case strings.HasSuffix(word, "ais"):
		word = word[:len(word)-3] + "al"
	case strings.HasSuffix(word, "eis") && len(word) > 5:
		word = word[:len(word)-3] + "el"
	case strings.HasSuffix(word, "res") && len(word) > 5:
		word = word[:len(word)-2]
	case strings.HasSuffix(word, "ns"):
		word = word[:len(word)-2] + "m"
	case strings.HasSuffix(word, "s") && len(word) > 4:
		word = word[:len(word)-1]
	}
	if w, ok := stripFirst(word, 3, "inha", "inho", "zinha", "zinho"); ok {
		word = w
	}
	if w, ok := stripFirst(word, 3, "a", "o", "e"); ok {
		return w
	}
	return word
}
