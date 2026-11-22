package reader

import (
	"bufio"
	"context"
	"encoding/csv"
	"io"
	"log/slog"
	"sync"
)

type ParallelReaderResult struct {
	Record map[string]string
	Err    error
}
type RawParallelReader interface {
	ReadParallel(ctx context.Context, workerCount int) (<-chan ParallelReaderResult, error)
}

func (cr *CSVReader) ReadParallel(ctx context.Context, workerCount int) (<-chan ParallelReaderResult, error) {
	out := make(chan ParallelReaderResult) // The output channel (streaming results)
	csvReader := csv.NewReader(cr.reader)

	headers, err := csvReader.Read()
	if err != nil {
		return nil, err
	}

	// Buffered job channel to allow decoupling read/processing
	jobs := make(chan []string, workerCount*2)
	var wg sync.WaitGroup

	// Start worker goroutines
	wg.Add(workerCount)
	for w := 0; w < workerCount; w++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case row, ok := <-jobs:
					if !ok {
						return
					}
					if len(row) != len(headers) {
						select {
						case out <- ParallelReaderResult{Err: io.ErrUnexpectedEOF}:
						case <-ctx.Done():
						}
						continue
					}
					record := make(map[string]string, len(headers))
					for i, h := range headers {
						record[h] = row[i]
					}
					select {
					case out <- ParallelReaderResult{Record: record}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	// Feed jobs into the channel in a separate goroutine
	go func() {
		defer close(jobs)

		for {
			row, err := csvReader.Read()
			if err == io.EOF {
				return
			}
			if err != nil {
				select {
				case out <- ParallelReaderResult{Err: err}:
				case <-ctx.Done():
					slog.Info("Context cancelled, stopping CSV read...")
				}
				slog.Error("Error reading CSV row", "error", err)
				continue
			}
			jobs <- row
		}
	}()

	go func() {
		wg.Wait()
		close(out)
	}()

	return out, nil
}

func (jr *JSONLReader) ReadParallel(ctx context.Context, workerCount int) (<-chan ParallelReaderResult, error) {
	out := make(chan ParallelReaderResult)

	jobs := make(chan []byte, workerCount*2)
	var wg sync.WaitGroup

	wg.Add(workerCount)
	for w := 0; w < workerCount; w++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-jobs:
					if !ok {
						return
					}
					record, err := decodeJSONLine(line)
					select {
					case out <- ParallelReaderResult{Record: record, Err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		scanner := bufio.NewScanner(jr.reader)
		scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			cp := make([]byte, len(line))
			copy(cp, line)
			select {
			case jobs <- cp:
			case <-ctx.Done():
				slog.Info("Context cancelled, stopping JSONL read...")
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case out <- ParallelReaderResult{Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	go func() {
		wg.Wait()
		close(out)
	}()

	return out, nil
}

func (p *ParquetReader) ReadParallel(ctx context.Context, workerCount int) (<-chan ParallelReaderResult, error) {
	rowGroups := p.file.RowGroups()
	out := make(chan ParallelReaderResult)

	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(rowGroups) {
		workerCount = len(rowGroups)
	}

	jobs := make(chan int, len(rowGroups))
	for i := range rowGroups {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	wg.Add(workerCount)
	for w := 0; w < workerCount; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				err := p.eachRecord(rowGroups[i], func(record map[string]string) error {
					select {
					case out <- ParallelReaderResult{Record: record}:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
				if err == nil {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				select {
				case out <- ParallelReaderResult{Err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out, nil
}
