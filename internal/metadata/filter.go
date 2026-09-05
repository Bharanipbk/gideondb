// Package metadata implements typed metadata filters and per-segment indexes.
package metadata

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/vectordb/vectordb/internal/core"
)

type Operator uint8

const (
	OpEq Operator = iota + 1
	OpNe
	OpGT
	OpGTE
	OpLT
	OpLTE
	OpIn
	OpNotIn
	OpExists
)

type Kind uint8

const (
	KindPredicate Kind = iota + 1
	KindAnd
	KindOr
	KindNot
)

type Expr struct {
	Kind     Kind
	Field    string
	Operator Operator
	Value    any
	Children []*Expr
}

// Set is a dense ordinal bitmap. Segment ordinals are compact and monotonically
// allocated, making this substantially smaller than map[uint64]struct{}.
type Set struct {
	words []uint64
	count int
}

func newSet(maxID uint64) *Set {
	return &Set{words: make([]uint64, maxID/64+1)}
}

func (s *Set) Add(id uint64) {
	word := int(id / 64)
	if word >= len(s.words) {
		s.words = append(s.words, make([]uint64, word-len(s.words)+1)...)
	}
	mask := uint64(1) << (id % 64)
	if s.words[word]&mask == 0 {
		s.words[word] |= mask
		s.count++
	}
}

func (s *Set) Remove(id uint64) {
	if s == nil || int(id/64) >= len(s.words) {
		return
	}
	word, mask := id/64, uint64(1)<<(id%64)
	if s.words[word]&mask != 0 {
		s.words[word] &^= mask
		s.count--
	}
}

func (s *Set) Contains(id uint64) bool {
	return s != nil && int(id/64) < len(s.words) && s.words[id/64]&(uint64(1)<<(id%64)) != 0
}

func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return s.count
}

func Parse(raw map[string]any) (*Expr, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	children := make([]*Expr, 0, len(raw))
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := raw[key]
		switch key {
		case "$and", "$or":
			items, ok := value.([]any)
			if !ok || len(items) == 0 {
				return nil, invalid("%s requires a non-empty array", key)
			}
			logical := &Expr{Kind: KindAnd}
			if key == "$or" {
				logical.Kind = KindOr
			}
			for _, item := range items {
				object, ok := item.(map[string]any)
				if !ok {
					return nil, invalid("%s entries must be objects", key)
				}
				child, err := Parse(object)
				if err != nil {
					return nil, err
				}
				logical.Children = append(logical.Children, child)
			}
			children = append(children, logical)
		case "$not":
			object, ok := value.(map[string]any)
			if !ok {
				return nil, invalid("$not requires an object")
			}
			child, err := Parse(object)
			if err != nil {
				return nil, err
			}
			children = append(children, &Expr{Kind: KindNot, Children: []*Expr{child}})
		default:
			if strings.HasPrefix(key, "$") || key == "" {
				return nil, invalid("unknown filter operator %q", key)
			}
			fieldExpr, err := parseField(key, value)
			if err != nil {
				return nil, err
			}
			children = append(children, fieldExpr)
		}
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &Expr{Kind: KindAnd, Children: children}, nil
}

func parseField(field string, value any) (*Expr, error) {
	operators, ok := value.(map[string]any)
	if !ok {
		if err := validateScalar(value); err != nil {
			return nil, err
		}
		return &Expr{Kind: KindPredicate, Field: field, Operator: OpEq, Value: value}, nil
	}
	if len(operators) == 0 {
		return nil, invalid("field %q has no operators", field)
	}
	children := make([]*Expr, 0, len(operators))
	for name, operand := range operators {
		op, err := parseOperator(name)
		if err != nil {
			return nil, err
		}
		if op == OpIn || op == OpNotIn {
			items, ok := operand.([]any)
			if !ok || len(items) == 0 {
				return nil, invalid("%s requires a non-empty array", name)
			}
			for _, item := range items {
				if err := validateScalar(item); err != nil {
					return nil, err
				}
			}
		} else if op == OpExists {
			if _, ok := operand.(bool); !ok {
				return nil, invalid("$exists requires a boolean")
			}
		} else if err := validateScalar(operand); err != nil {
			return nil, err
		}
		children = append(children, &Expr{Kind: KindPredicate, Field: field, Operator: op, Value: operand})
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return &Expr{Kind: KindAnd, Children: children}, nil
}

func parseOperator(value string) (Operator, error) {
	switch value {
	case "$eq":
		return OpEq, nil
	case "$ne":
		return OpNe, nil
	case "$gt":
		return OpGT, nil
	case "$gte":
		return OpGTE, nil
	case "$lt":
		return OpLT, nil
	case "$lte":
		return OpLTE, nil
	case "$in":
		return OpIn, nil
	case "$nin":
		return OpNotIn, nil
	case "$exists":
		return OpExists, nil
	default:
		return 0, invalid("unknown field operator %q", value)
	}
}

func validateScalar(value any) error {
	switch value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case float32, float64, json.Number:
		if _, ok := number(value); ok {
			return nil
		}
		return invalid("numeric filter values must be finite")
	default:
		return invalid("filter values must be JSON scalars")
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", core.ErrInvalidArgument, fmt.Sprintf(format, args...))
}

type Index struct {
	universe *Set
	values   map[string]map[uint64]any
	postings map[string]map[string]*posting
	present  map[string]*Set
	numeric  map[string]*numericField
}

type numericEntry struct {
	value float64
	id    uint64
}

// numericField is a miniature LSM structure: base is immutable between merges
// and delta accepts O(1) appends. current is authoritative for superseded and
// deleted entries that may still exist in base or delta.
type numericField struct {
	base        []numericEntry
	delta       []numericEntry
	current     map[uint64]float64
	invalidBase *Set
	stale       int
}

// posting avoids allocating a full dense bitmap for singleton and low-
// cardinality values. It promotes to a bitmap once word-wise operations become
// cheaper than sparse membership.
type posting struct {
	singleton uint64
	sparse    map[uint64]struct{}
	dense     *Set
	count     int
}

func (p *posting) Add(id uint64) {
	if p.dense != nil {
		before := p.dense.Len()
		p.dense.Add(id)
		p.count += p.dense.Len() - before
		return
	}
	if p.count == 0 {
		p.singleton, p.count = id, 1
		return
	}
	if p.count == 1 {
		if p.singleton == id {
			return
		}
		p.sparse = map[uint64]struct{}{p.singleton: {}, id: {}}
		p.count = 2
		return
	}
	if _, exists := p.sparse[id]; exists {
		return
	}
	p.sparse[id] = struct{}{}
	p.count++
	if p.count >= 64 {
		p.dense = newSet(id)
		for value := range p.sparse {
			p.dense.Add(value)
		}
		p.sparse = nil
	}
}

func (p *posting) Remove(id uint64) {
	if p == nil || p.count == 0 {
		return
	}
	if p.dense != nil {
		before := p.dense.Len()
		p.dense.Remove(id)
		p.count -= before - p.dense.Len()
		return
	}
	if p.count == 1 {
		if p.singleton == id {
			p.count = 0
		}
		return
	}
	if _, exists := p.sparse[id]; exists {
		delete(p.sparse, id)
		p.count--
	}
	if p.count == 1 {
		for value := range p.sparse {
			p.singleton = value
		}
		p.sparse = nil
	}
}

func (p *posting) Set(maxID uint64) *Set {
	if p == nil || p.count == 0 {
		return newSet(0)
	}
	if p.dense != nil {
		return clone(p.dense)
	}
	result := newSet(maxID)
	if p.count == 1 {
		result.Add(p.singleton)
		return result
	}
	for id := range p.sparse {
		result.Add(id)
	}
	return result
}

func NewIndex() *Index {
	return &Index{
		universe: newSet(0), values: make(map[string]map[uint64]any),
		postings: make(map[string]map[string]*posting), present: make(map[string]*Set),
		numeric: make(map[string]*numericField),
	}
}

func (i *Index) Upsert(id uint64, metadata map[string]any) {
	i.Delete(id)
	i.universe.Add(id)
	for field, value := range metadata {
		if validateScalar(value) != nil {
			continue
		}
		if i.values[field] == nil {
			i.values[field] = make(map[uint64]any)
		}
		i.values[field][id] = value
		key := canonical(value)
		if i.postings[field] == nil {
			i.postings[field] = make(map[string]*posting)
		}
		if i.postings[field][key] == nil {
			i.postings[field][key] = &posting{}
		}
		i.postings[field][key].Add(id)
		if i.present[field] == nil {
			i.present[field] = newSet(id)
		}
		i.present[field].Add(id)
		if value, ok := number(value); ok {
			i.insertNumeric(field, numericEntry{value: value, id: id})
		}
	}
}

func (i *Index) Delete(id uint64) {
	i.universe.Remove(id)
	for field, values := range i.values {
		value, ok := values[id]
		if !ok {
			continue
		}
		delete(values, id)
		key := canonical(value)
		i.postings[field][key].Remove(id)
		i.present[field].Remove(id)
		if numeric, ok := number(value); ok {
			i.deleteNumeric(field, numeric, id)
		}
	}
}

func (i *Index) Evaluate(expr *Expr) *Set {
	if expr == nil {
		return clone(i.universe)
	}
	switch expr.Kind {
	case KindAnd:
		result := clone(i.universe)
		for _, child := range expr.Children {
			result = intersect(result, i.Evaluate(child))
		}
		return result
	case KindOr:
		result := newSet(uint64(len(i.universe.words) * 64))
		for _, child := range expr.Children {
			result = union(result, i.Evaluate(child))
		}
		return result
	case KindNot:
		return difference(i.universe, i.Evaluate(expr.Children[0]))
	case KindPredicate:
		return i.predicate(expr)
	default:
		return newSet(0)
	}
}

func (i *Index) predicate(expr *Expr) *Set {
	values := i.values[expr.Field]
	switch expr.Operator {
	case OpExists:
		existing := clone(i.present[expr.Field])
		if expr.Value.(bool) {
			return existing
		}
		return difference(i.universe, existing)
	case OpEq:
		return i.postingSet(expr.Field, expr.Value)
	case OpNe:
		return difference(i.present[expr.Field], i.postingSet(expr.Field, expr.Value))
	case OpIn, OpNotIn:
		matched := newSet(uint64(len(i.universe.words) * 64))
		for _, value := range expr.Value.([]any) {
			matched = union(matched, i.postingSet(expr.Field, value))
		}
		if expr.Operator == OpNotIn {
			return difference(i.present[expr.Field], matched)
		}
		return matched
	default:
		if target, ok := number(expr.Value); ok {
			return i.numericRange(expr.Field, expr.Operator, target)
		}
		result := newSet(uint64(len(i.universe.words) * 64))
		for id, value := range values {
			comparison, comparable := compare(value, expr.Value)
			if comparable && ((expr.Operator == OpGT && comparison > 0) || (expr.Operator == OpGTE && comparison >= 0) ||
				(expr.Operator == OpLT && comparison < 0) || (expr.Operator == OpLTE && comparison <= 0)) {
				result.Add(id)
			}
		}
		return result
	}
}

func (i *Index) postingSet(field string, value any) *Set {
	maxID := uint64(0)
	if len(i.universe.words) > 0 {
		maxID = uint64(len(i.universe.words)*64 - 1)
	}
	return i.postings[field][canonical(value)].Set(maxID)
}

func (i *Index) insertNumeric(field string, entry numericEntry) {
	index := i.numeric[field]
	if index == nil {
		index = &numericField{current: make(map[uint64]float64), invalidBase: newSet(0)}
		i.numeric[field] = index
	}
	index.current[entry.id] = entry.value
	index.delta = append(index.delta, entry)
	index.mergeIfNeeded()
}

func (i *Index) deleteNumeric(field string, value float64, id uint64) {
	index := i.numeric[field]
	if index == nil {
		return
	}
	if current, ok := index.current[id]; ok && current == value {
		delete(index.current, id)
		index.invalidBase.Add(id)
		index.stale++
		index.mergeIfNeeded()
	}
}

func (i *Index) numericRange(field string, operator Operator, target float64) *Set {
	index := i.numeric[field]
	result := newSet(uint64(len(i.universe.words) * 64))
	if index == nil {
		return result
	}
	start, end := 0, len(index.base)
	switch operator {
	case OpGT:
		start = sort.Search(len(index.base), func(position int) bool { return index.base[position].value > target })
	case OpGTE:
		start = sort.Search(len(index.base), func(position int) bool { return index.base[position].value >= target })
	case OpLT:
		end = sort.Search(len(index.base), func(position int) bool { return index.base[position].value >= target })
	case OpLTE:
		end = sort.Search(len(index.base), func(position int) bool { return index.base[position].value > target })
	}
	for _, entry := range index.base[start:end] {
		if !index.invalidBase.Contains(entry.id) {
			result.Add(entry.id)
		}
	}
	for _, entry := range index.delta {
		if current, ok := index.current[entry.id]; ok && current == entry.value && rangeMatches(entry.value, operator, target) {
			result.Add(entry.id)
		}
	}
	return result
}

func (n *numericField) mergeIfNeeded() {
	const deltaLimit = 4096
	if len(n.delta) < deltaLimit && n.stale < deltaLimit {
		return
	}
	n.base = n.base[:0]
	if cap(n.base) < len(n.current) {
		n.base = make([]numericEntry, 0, len(n.current))
	}
	for id, value := range n.current {
		n.base = append(n.base, numericEntry{value: value, id: id})
	}
	sort.Slice(n.base, func(left, right int) bool {
		return n.base[left].value < n.base[right].value || (n.base[left].value == n.base[right].value && n.base[left].id < n.base[right].id)
	})
	n.delta = n.delta[:0]
	n.invalidBase = newSet(0)
	n.stale = 0
}

func rangeMatches(value float64, operator Operator, target float64) bool {
	switch operator {
	case OpGT:
		return value > target
	case OpGTE:
		return value >= target
	case OpLT:
		return value < target
	case OpLTE:
		return value <= target
	default:
		return false
	}
}

func canonical(value any) string {
	if value == nil {
		return "n:"
	}
	switch value := value.(type) {
	case string:
		return "s:" + value
	case bool:
		return "b:" + strconv.FormatBool(value)
	case int:
		return "d:" + strconv.FormatInt(int64(value), 10)
	case int8:
		return "d:" + strconv.FormatInt(int64(value), 10)
	case int16:
		return "d:" + strconv.FormatInt(int64(value), 10)
	case int32:
		return "d:" + strconv.FormatInt(int64(value), 10)
	case int64:
		return "d:" + strconv.FormatInt(value, 10)
	case uint:
		return "d:" + strconv.FormatUint(uint64(value), 10)
	case uint8:
		return "d:" + strconv.FormatUint(uint64(value), 10)
	case uint16:
		return "d:" + strconv.FormatUint(uint64(value), 10)
	case uint32:
		return "d:" + strconv.FormatUint(uint64(value), 10)
	case uint64:
		return "d:" + strconv.FormatUint(value, 10)
	case float32:
		return "d:" + strconv.FormatFloat(float64(value), 'g', -1, 32)
	case float64:
		return "d:" + strconv.FormatFloat(value, 'g', -1, 64)
	case json.Number:
		if integer, err := strconv.ParseInt(string(value), 10, 64); err == nil {
			return "d:" + strconv.FormatInt(integer, 10)
		}
		if unsigned, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			return "d:" + strconv.FormatUint(unsigned, 10)
		}
		if parsed, err := value.Float64(); err == nil {
			return "d:" + strconv.FormatFloat(parsed, 'g', -1, 64)
		}
		return "?:" + string(value)
	default:
		return "?:" + fmt.Sprint(value)
	}
}

func compare(left, right any) (int, bool) {
	if a, ok := number(left); ok {
		b, ok := number(right)
		if !ok {
			return 0, false
		}
		if a < b {
			return -1, true
		}
		if a > b {
			return 1, true
		}
		return 0, true
	}
	a, aok := left.(string)
	b, bok := right.(string)
	if aok && bok {
		return strings.Compare(a, b), true
	}
	return 0, false
}

func number(value any) (float64, bool) {
	var result float64
	switch value := value.(type) {
	case float64:
		result = value
	case float32:
		result = float64(value)
	case int:
		result = float64(value)
	case int8:
		result = float64(value)
	case int16:
		result = float64(value)
	case int32:
		result = float64(value)
	case int64:
		result = float64(value)
	case uint:
		result = float64(value)
	case uint8:
		result = float64(value)
	case uint16:
		result = float64(value)
	case uint32:
		result = float64(value)
	case uint64:
		result = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false
		}
		result = parsed
	default:
		return 0, false
	}
	return result, !math.IsNaN(result) && !math.IsInf(result, 0)
}

func clone(source *Set) *Set {
	if source == nil {
		return newSet(0)
	}
	return &Set{words: append([]uint64(nil), source.words...), count: source.count}
}
func union(a, b *Set) *Set {
	result := clone(a)
	if b == nil {
		return result
	}
	if len(result.words) < len(b.words) {
		result.words = append(result.words, make([]uint64, len(b.words)-len(result.words))...)
	}
	for position, word := range b.words {
		result.words[position] |= word
	}
	result.recount()
	return result
}
func intersect(a, b *Set) *Set {
	if a == nil || b == nil {
		return newSet(0)
	}
	length := min(len(a.words), len(b.words))
	result := &Set{words: make([]uint64, length)}
	for position := range length {
		result.words[position] = a.words[position] & b.words[position]
	}
	result.recount()
	return result
}
func difference(a, b *Set) *Set {
	result := clone(a)
	if b == nil {
		return result
	}
	for position := 0; position < min(len(result.words), len(b.words)); position++ {
		result.words[position] &^= b.words[position]
	}
	result.recount()
	return result
}
func (s *Set) recount() {
	s.count = 0
	for _, word := range s.words {
		s.count += bits.OnesCount64(word)
	}
}
