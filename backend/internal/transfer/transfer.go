// Package transfer implements resumable multipart artifact transfers from the
// brain node to worker nodes over the persistent mTLS yamux tunnel.
//
// Design:
//   - The brain splits an artifact archive into fixed 4 MB chunks.
//   - Each chunk is POSTed to the worker's /api/node/artifact-chunk/{transferID}/{chunkN}
//     endpoint via the tunnel proxy.
//   - The worker stores each chunk to disk under {dataDir}/transfers/{transferID}/chunk-{n}.
//   - On receiving the final chunk, the worker reassembles the archive and extracts it.
//   - If the connection drops mid-transfer, the brain resumes from the last confirmed chunk.
package transfer

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const DefaultChunkSize = 4 * 1024 * 1024 // 4 MB

// Chunker splits a file into in-memory chunks for streaming.
type Chunker struct {
	FilePath  string
	ChunkSize int
}

// ChunkCount returns the total number of chunks for the file.
func (c *Chunker) ChunkCount() (int, int64, error) {
	info, err := os.Stat(c.FilePath)
	if err != nil {
		return 0, 0, fmt.Errorf("stat: %w", err)
	}
	sz := info.Size()
	if c.ChunkSize <= 0 {
		c.ChunkSize = DefaultChunkSize
	}
	count := int((sz + int64(c.ChunkSize) - 1) / int64(c.ChunkSize))
	if count == 0 {
		count = 1
	}
	return count, sz, nil
}

// ReadChunk reads chunk number n (0-indexed) and returns its bytes.
func (c *Chunker) ReadChunk(n int) ([]byte, error) {
	f, err := os.Open(c.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(int64(n)*int64(c.ChunkSize), io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}
	buf := make([]byte, c.ChunkSize)
	nr, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return buf[:nr], nil
	}
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return buf[:nr], nil
}

// Assembler reassembles chunks written to disk into a final file.
type Assembler struct {
	DataDir    string // base data directory
	TransferID int64
}

// ChunkDir returns the directory where chunks for this transfer are stored.
func (a *Assembler) ChunkDir() string {
	return filepath.Join(a.DataDir, "transfers", strconv.FormatInt(a.TransferID, 10))
}

// WriteChunk writes a single chunk to disk. Safe to call concurrently for
// different chunk numbers. Calling for the same chunk number is idempotent.
func (a *Assembler) WriteChunk(chunkN int, data []byte) error {
	dir := a.ChunkDir()
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("chunk-%d", chunkN))
	return os.WriteFile(path, data, 0640)
}

// ReceivedChunks returns a sorted list of chunk numbers already on disk.
func (a *Assembler) ReceivedChunks() ([]int, error) {
	entries, err := os.ReadDir(a.ChunkDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var nums []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "chunk-") {
			continue
		}
		n, err := strconv.Atoi(name[6:])
		if err == nil {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums, nil
}

// Assemble concatenates all chunks in order into destFile.
// Returns an error if any chunk is missing.
func (a *Assembler) Assemble(totalChunks int, destFile string) error {
	chunks, err := a.ReceivedChunks()
	if err != nil {
		return fmt.Errorf("list chunks: %w", err)
	}
	if len(chunks) != totalChunks {
		return fmt.Errorf("incomplete: have %d/%d chunks", len(chunks), totalChunks)
	}

	out, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	dir := a.ChunkDir()
	for i := 0; i < totalChunks; i++ {
		chunkPath := filepath.Join(dir, fmt.Sprintf("chunk-%d", i))
		data, err := os.ReadFile(chunkPath)
		if err != nil {
			return fmt.Errorf("read chunk %d: %w", i, err)
		}
		if _, err := out.Write(data); err != nil {
			return fmt.Errorf("write chunk %d: %w", i, err)
		}
	}
	slog.Info("transfer: assembled artifact", "transfer_id", a.TransferID, "dest", destFile, "chunks", totalChunks)
	return nil
}

// Cleanup removes the chunk directory after successful assembly.
func (a *Assembler) Cleanup() error {
	return os.RemoveAll(a.ChunkDir())
}
