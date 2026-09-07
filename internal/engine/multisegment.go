package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/storage/compaction"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
)

func manifestForRef(ref segmentfile.SegmentRef) segmentfile.Manifest {
	return segmentfile.Manifest{Format: 2, RecordsFile: ref.RecordsFile, VectorsFile: ref.VectorsFile, GraphFile: ref.GraphFile, FilterFile: ref.FilterFile, Dimension: ref.Dimension, MaxLSN: ref.MaxLSN, RecordCount: ref.RecordCount}
}

func refFromManifest(directory string, manifest segmentfile.Manifest) (segmentfile.SegmentRef, error) {
	ref := segmentfile.SegmentRef{RecordsFile: manifest.RecordsFile, VectorsFile: manifest.VectorsFile, GraphFile: manifest.GraphFile, FilterFile: manifest.FilterFile, Dimension: manifest.Dimension, MaxLSN: manifest.MaxLSN, RecordCount: manifest.RecordCount}
	for _, name := range []string{ref.RecordsFile, ref.VectorsFile, ref.GraphFile, ref.FilterFile} {
		if name == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			return segmentfile.SegmentRef{}, err
		}
		ref.SizeBytes += uint64(info.Size())
	}
	return ref, nil
}

func materializeSegments(directory string, refs []segmentfile.SegmentRef) ([]core.Record, []string, error) {
	records := make(map[string]core.Record)
	deleted := make(map[string]struct{})
	for _, ref := range refs {
		items, err := segmentfile.ReadBundle(directory, manifestForRef(ref))
		if err != nil {
			return nil, nil, err
		}
		for _, record := range items {
			key := record.Namespace + "\x00" + record.ID
			records[key] = record
			delete(deleted, key)
		}
		if ref.TombstonesFile != "" {
			keys, err := segmentfile.ReadTombstones(directory, ref.TombstonesFile, ref.MaxLSN)
			if err != nil {
				return nil, nil, err
			}
			for _, key := range keys {
				delete(records, key)
				deleted[key] = struct{}{}
			}
		}
	}
	items := make([]core.Record, 0, len(records))
	for _, record := range records {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Namespace < items[j].Namespace || (items[i].Namespace == items[j].Namespace && items[i].ID < items[j].ID)
	})
	tombstones := make([]string, 0, len(deleted))
	for key := range deleted {
		tombstones = append(tombstones, key)
	}
	sort.Strings(tombstones)
	return items, tombstones, nil
}

func resolveMappedSegments(directory string, refs []segmentfile.SegmentRef) ([]core.Record, []segmentfile.VectorLocation, error) {
	type selectedRecord struct {
		record   core.Record
		location segmentfile.VectorLocation
	}
	selected := make(map[string]selectedRecord)
	for segmentID, ref := range refs {
		items, err := segmentfile.ReadRecordColumn(directory, manifestForRef(ref))
		if err != nil {
			return nil, nil, err
		}
		for ordinal, record := range items {
			selected[record.Namespace+"\x00"+record.ID] = selectedRecord{record: record, location: segmentfile.VectorLocation{Segment: segmentID, Ordinal: ordinal}}
		}
		if ref.TombstonesFile != "" {
			keys, err := segmentfile.ReadTombstones(directory, ref.TombstonesFile, ref.MaxLSN)
			if err != nil {
				return nil, nil, err
			}
			for _, key := range keys {
				delete(selected, key)
			}
		}
	}
	values := make([]selectedRecord, 0, len(selected))
	for _, value := range selected {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].record.Namespace < values[j].record.Namespace || (values[i].record.Namespace == values[j].record.Namespace && values[i].record.ID < values[j].record.ID)
	})
	records := make([]core.Record, len(values))
	locations := make([]segmentfile.VectorLocation, len(values))
	for position, value := range values {
		records[position], locations[position] = value.record, value.location
	}
	return records, locations, nil
}

func writeSegmentRef(directory, base string, records []core.Record, tombstones []string, dimension int, maxLSN uint64) (segmentfile.SegmentRef, error) {
	bundle, err := segmentfile.WriteBundle(directory, base, records, dimension, maxLSN)
	if err != nil {
		return segmentfile.SegmentRef{}, err
	}
	ref := segmentfile.SegmentRef{RecordsFile: bundle.RecordsFile, VectorsFile: bundle.VectorsFile, Dimension: bundle.Dimension, MaxLSN: maxLSN, RecordCount: uint64(len(records))}
	if len(tombstones) > 0 {
		ref.TombstonesFile = base + ".tombstones"
		if err := segmentfile.WriteTombstones(directory, ref.TombstonesFile, tombstones, maxLSN); err != nil {
			return segmentfile.SegmentRef{}, err
		}
	}
	for _, name := range []string{ref.RecordsFile, ref.VectorsFile, ref.TombstonesFile} {
		if name == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			return segmentfile.SegmentRef{}, err
		}
		ref.SizeBytes += uint64(info.Size())
	}
	return ref, nil
}

func compactSegmentRefs(directory string, refs []segmentfile.SegmentRef, dimension int, sequence uint64) ([]segmentfile.SegmentRef, error) {
	policy := compaction.DefaultPolicy()
	metadata := make([]compaction.Segment, len(refs))
	for position, ref := range refs {
		metadata[position] = compaction.Segment{ID: ref.RecordsFile, MaxLSN: ref.MaxLSN, SizeBytes: ref.SizeBytes}
	}
	plan, err := policy.Select(metadata)
	if err != nil || len(plan.Inputs) == 0 {
		return refs, err
	}
	selected := make(map[string]struct{}, len(plan.Inputs))
	for _, input := range plan.Inputs {
		selected[input.ID] = struct{}{}
	}
	inputs := make([]segmentfile.SegmentRef, 0, len(plan.Inputs))
	remaining := make([]segmentfile.SegmentRef, 0, len(refs)-len(plan.Inputs)+1)
	insertAt := -1
	for _, ref := range refs {
		if _, ok := selected[ref.RecordsFile]; ok {
			if insertAt < 0 {
				insertAt = len(remaining)
			}
			inputs = append(inputs, ref)
		} else {
			remaining = append(remaining, ref)
		}
	}
	records, tombstones, err := materializeSegments(directory, inputs)
	if err != nil {
		return nil, err
	}
	maxLSN := inputs[len(inputs)-1].MaxLSN
	merged, err := writeSegmentRef(directory, fmt.Sprintf("segment-%020d-compact-%020d", maxLSN, sequence), records, tombstones, dimension, maxLSN)
	if err != nil {
		return nil, err
	}
	remaining = append(remaining, segmentfile.SegmentRef{})
	copy(remaining[insertAt+1:], remaining[insertAt:])
	remaining[insertAt] = merged
	return remaining, nil
}
