package compaction

import (
	"fmt"
	"testing"
)

func TestSelectsOldestSizeTierWithinBound(t *testing.T) {
	policy := DefaultPolicy()
	segments := []Segment{
		{ID: "new", MaxLSN: 50, SizeBytes: 8 << 20},
		{ID: "a", MaxLSN: 10, SizeBytes: 2 << 20},
		{ID: "b", MaxLSN: 20, SizeBytes: 3 << 20},
		{ID: "c", MaxLSN: 30, SizeBytes: 4 << 20},
		{ID: "d", MaxLSN: 40, SizeBytes: 5 << 20},
	}
	plan, err := policy.Select(segments)
	if err != nil || len(plan.Inputs) != 4 || plan.Inputs[0].ID != "a" || plan.InputBytes != 14<<20 || plan.Forced {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
}

func TestPolicyDefersAndBoundsForcedPlan(t *testing.T) {
	policy := Policy{FanIn: 3, SizeRatio: 1, MaxInputBytes: 10, MaxSegments: 4}
	plan, err := policy.Select([]Segment{{ID: "a", MaxLSN: 1, SizeBytes: 2}, {ID: "b", MaxLSN: 2, SizeBytes: 4}})
	if err != nil || len(plan.Inputs) != 0 {
		t.Fatalf("unexpected early plan = %#v, %v", plan, err)
	}
	plan, err = policy.Select([]Segment{{ID: "a", MaxLSN: 1, SizeBytes: 4}, {ID: "b", MaxLSN: 2, SizeBytes: 4}, {ID: "c", MaxLSN: 3, SizeBytes: 20}, {ID: "d", MaxLSN: 4, SizeBytes: 20}})
	if err != nil || !plan.Forced || len(plan.Inputs) != 2 || plan.InputBytes > policy.MaxInputBytes {
		t.Fatalf("forced plan = %#v, %v", plan, err)
	}
}

func TestPolicyRejectsUnboundedSegmentPressure(t *testing.T) {
	policy := Policy{FanIn: 2, SizeRatio: 2, MaxInputBytes: 10, MaxSegments: 2}
	if _, err := policy.Select([]Segment{{ID: "a", MaxLSN: 1, SizeBytes: 20}, {ID: "b", MaxLSN: 2, SizeBytes: 20}}); err == nil {
		t.Fatal("expected backpressure error")
	}
}

func TestSizeTieredWriteAmplificationBelowFullRewrite(t *testing.T) {
	policy := DefaultPolicy()
	policy.MaxInputBytes = 1 << 40
	var segments []Segment
	var tieredBytes uint64
	const flushes = 64
	for lsn := uint64(1); lsn <= flushes; lsn++ {
		segments = append(segments, Segment{ID: fmt.Sprintf("flush-%d", lsn), MaxLSN: lsn, SizeBytes: 1})
		tieredBytes++
		for {
			plan, err := policy.Select(segments)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Inputs) == 0 {
				break
			}
			tieredBytes += plan.InputBytes
			selected := make(map[string]struct{}, len(plan.Inputs))
			for _, input := range plan.Inputs {
				selected[input.ID] = struct{}{}
			}
			remaining := segments[:0]
			for _, segment := range segments {
				if _, compacted := selected[segment.ID]; !compacted {
					remaining = append(remaining, segment)
				}
			}
			segments = append(remaining, Segment{ID: fmt.Sprintf("compact-%d-%d", lsn, len(segments)), MaxLSN: lsn, SizeBytes: plan.InputBytes})
		}
	}
	fullRewriteBytes := uint64(flushes*(flushes+1)) / 2
	if len(segments) > policy.MaxSegments || tieredBytes >= fullRewriteBytes/2 {
		t.Fatalf("segments=%d tiered=%d full=%d", len(segments), tieredBytes, fullRewriteBytes)
	}
	t.Logf("64 unit flushes: size-tiered writes=%d, full rewrites=%d, ratio=%.3f", tieredBytes, fullRewriteBytes, float64(tieredBytes)/float64(fullRewriteBytes))
}

func BenchmarkSelectEightSegments(b *testing.B) {
	policy := DefaultPolicy()
	segments := []Segment{{ID: "1", MaxLSN: 1, SizeBytes: 1 << 20}, {ID: "2", MaxLSN: 2, SizeBytes: 2 << 20}, {ID: "3", MaxLSN: 3, SizeBytes: 2 << 20}, {ID: "4", MaxLSN: 4, SizeBytes: 3 << 20}, {ID: "5", MaxLSN: 5, SizeBytes: 12 << 20}, {ID: "6", MaxLSN: 6, SizeBytes: 13 << 20}, {ID: "7", MaxLSN: 7, SizeBytes: 14 << 20}, {ID: "8", MaxLSN: 8, SizeBytes: 15 << 20}}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = policy.Select(segments)
	}
}
