// Package wal implements the per-logical-shard write-ahead log.
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	headerSize     = 28
	formatVersion  = 1
	maximumPayload = 64 << 20
)

var magic = [4]byte{'V', 'D', 'B', 'W'}
var crcTable = crc32.MakeTable(crc32.Castagnoli)

type SyncMode string

const (
	SyncAlways SyncMode = "always"
	SyncAsync  SyncMode = "async"
)

type Operation uint8

const (
	OperationUpsert      Operation = 1
	OperationDelete      Operation = 2
	OperationBatchUpsert Operation = 3
)

type Record struct {
	LSN       uint64
	Operation Operation
	Payload   []byte
}

type Log struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	mode    SyncMode
	nextLSN uint64
}

// Open validates and replays an existing log before opening it for append.
func Open(path string, mode SyncMode, apply func(Record) error) (*Log, error) {
	return OpenWithOptions(path, OpenOptions{SyncMode: mode, InitialLSN: 1}, apply)
}

type OpenOptions struct {
	SyncMode    SyncMode
	InitialLSN  uint64
	ReplayAfter uint64
}

func OpenWithOptions(path string, options OpenOptions, apply func(Record) error) (*Log, error) {
	mode := options.SyncMode
	if mode != SyncAlways && mode != SyncAsync {
		return nil, fmt.Errorf("unsupported WAL sync mode %q", mode)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, statErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	if created {
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := syncDirectory(directory); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if options.InitialLSN == 0 {
		options.InitialLSN = 1
	}
	lastLSN, validBytes, err := recoverFile(file, options.ReplayAfter, apply)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Size() != validBytes {
		// Only a partial final record reaches this path. Mid-log corruption is
		// rejected by recoverFile and is never truncated automatically.
		if err := file.Truncate(validBytes); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return nil, err
	}
	nextLSN := lastLSN + 1
	if lastLSN == 0 {
		nextLSN = options.InitialLSN
	}
	return &Log{file: file, path: path, mode: mode, nextLSN: nextLSN}, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (l *Log) Append(operation Operation, payload []byte) (uint64, error) {
	if operation != OperationUpsert && operation != OperationDelete && operation != OperationBatchUpsert {
		return 0, fmt.Errorf("invalid WAL operation %d", operation)
	}
	if len(payload) > maximumPayload {
		return 0, fmt.Errorf("WAL payload exceeds %d bytes", maximumPayload)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lsn := l.nextLSN
	start, err := l.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	header := encodeHeader(lsn, operation, payload)
	buffers := netBuffers{header[:], payload}
	if _, err := buffers.writeTo(l.file); err != nil {
		l.rollbackLocked(start)
		return 0, fmt.Errorf("append WAL %s: %w", l.path, err)
	}
	if l.mode == SyncAlways {
		if err := l.file.Sync(); err != nil {
			l.rollbackLocked(start)
			return 0, fmt.Errorf("sync WAL %s: %w", l.path, err)
		}
	}
	l.nextLSN++
	return lsn, nil
}

func (l *Log) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Sync()
}

func (l *Log) LastLSN() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.nextLSN == 0 {
		return 0
	}
	return l.nextLSN - 1
}

// Reset discards entries covered by a durable checkpoint and makes the next
// append use nextLSN. The caller must commit the checkpoint manifest first.
func (l *Log) Reset(nextLSN uint64) error {
	if nextLSN == 0 {
		return fmt.Errorf("next LSN must be positive")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := l.file.Sync(); err != nil {
		return err
	}
	l.nextLSN = nextLSN
	return nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	var syncErr error
	if l.mode == SyncAsync {
		syncErr = l.file.Sync()
	}
	closeErr := l.file.Close()
	l.file = nil
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (l *Log) rollbackLocked(offset int64) {
	_ = l.file.Truncate(offset)
	_, _ = l.file.Seek(offset, io.SeekStart)
}

func recoverFile(file *os.File, replayAfter uint64, apply func(Record) error) (lastLSN uint64, validBytes int64, err error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	header := make([]byte, headerSize)
	for {
		recordStart := validBytes
		_, readErr := io.ReadFull(file, header)
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return lastLSN, recordStart, nil
		}
		if readErr != nil {
			return 0, 0, readErr
		}
		if string(header[:4]) != string(magic[:]) {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: invalid magic", recordStart)
		}
		version := binary.LittleEndian.Uint16(header[4:6])
		if version != formatVersion {
			return 0, 0, fmt.Errorf("WAL version %d is unsupported", version)
		}
		if binary.LittleEndian.Uint16(header[6:8]) != 0 || header[21] != 0 || header[22] != 0 || header[23] != 0 {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: reserved fields are nonzero", recordStart)
		}
		length := binary.LittleEndian.Uint32(header[8:12])
		if length > maximumPayload {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: payload length %d", recordStart, length)
		}
		lsn := binary.LittleEndian.Uint64(header[12:20])
		operation := Operation(header[20])
		if operation != OperationUpsert && operation != OperationDelete && operation != OperationBatchUpsert {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: operation %d", recordStart, operation)
		}
		if lastLSN != 0 && lsn != lastLSN+1 {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: non-contiguous LSN %d after %d", recordStart, lsn, lastLSN)
		}
		payload := make([]byte, length)
		if _, readErr := io.ReadFull(file, payload); errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return lastLSN, recordStart, nil
		} else if readErr != nil {
			return 0, 0, readErr
		}
		wantCRC := binary.LittleEndian.Uint32(header[24:28])
		checksum := crc32.Update(0, crcTable, header[4:24])
		checksum = crc32.Update(checksum, crcTable, payload)
		if checksum != wantCRC {
			return 0, 0, fmt.Errorf("WAL corruption at offset %d: checksum mismatch", recordStart)
		}
		if apply != nil && lsn > replayAfter {
			copied := append([]byte(nil), payload...)
			if err := apply(Record{LSN: lsn, Operation: operation, Payload: copied}); err != nil {
				return 0, 0, fmt.Errorf("apply WAL LSN %d: %w", lsn, err)
			}
		}
		lastLSN = lsn
		validBytes = recordStart + headerSize + int64(length)
	}
}

func encodeHeader(lsn uint64, operation Operation, payload []byte) [headerSize]byte {
	var header [headerSize]byte
	copy(header[:4], magic[:])
	binary.LittleEndian.PutUint16(header[4:6], formatVersion)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	binary.LittleEndian.PutUint64(header[12:20], lsn)
	header[20] = byte(operation)
	checksum := crc32.Update(0, crcTable, header[4:24])
	checksum = crc32.Update(checksum, crcTable, payload)
	binary.LittleEndian.PutUint32(header[24:28], checksum)
	return header
}

// netBuffers is a tiny writev-like helper that handles short writes without an
// external dependency.
type netBuffers [][]byte

func (b netBuffers) writeTo(writer io.Writer) (int64, error) {
	var total int64
	for _, buffer := range b {
		for len(buffer) > 0 {
			n, err := writer.Write(buffer)
			total += int64(n)
			buffer = buffer[n:]
			if err != nil {
				return total, err
			}
			if n == 0 {
				return total, io.ErrShortWrite
			}
		}
	}
	return total, nil
}
