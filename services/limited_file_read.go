package services

import (
	"fmt"
	"io"
	"os"
)

// readFileLimited reads one byte beyond the limit so files that grow after a
// metadata check are still rejected instead of being silently truncated.
func readFileLimited(path string, maxSize int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes", maxSize)
	}
	return data, nil
}
