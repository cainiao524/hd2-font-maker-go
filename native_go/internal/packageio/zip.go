// Package packageio builds standard ZIP archives with bounded parallel DEFLATE
// and streaming CRC/SHA-256 readback, without a Python runtime.
package packageio

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// File is an input member. Path reads a file incrementally; otherwise Data is
// used, with nil meaning an empty member. Set at most one of Path and Data.
type File struct {
	Name string
	Path string
	Data []byte
}

type Progress struct {
	Done  int
	Total int
	Name  string
}

type Options struct {
	// WorkDir is the temporary directory's parent; empty uses the OS default.
	WorkDir string
	// Level is a compress/flate level: -2, -1, or 0 through 9. Zero is valid.
	Level int
	// Workers defaults to 2 and is capped at 2, matching package_io.py.
	Workers int
	// Progress runs serially after each member has been appended to the ZIP.
	Progress func(Progress)
}

type MemberInfo struct {
	Name           string
	Size           uint64
	CompressedSize uint64
	CRC32          uint32
	SHA256         string
}

type Report struct {
	Size    int64
	SHA256  string
	Members []MemberInfo
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type compressedMember struct {
	info MemberInfo
	path string
	err  error
}

func compressMember(ctx context.Context, file File, folder string, level int, buffer []byte) (result compressedMember) {
	result.info.Name = file.Name
	if result.err = ctx.Err(); result.err != nil {
		return result
	}
	var source io.Reader = bytes.NewReader(file.Data)
	if file.Path != "" {
		input, err := os.Open(file.Path)
		if err != nil {
			result.err = err
			return result
		}
		defer input.Close()
		source = input
	}
	output, err := os.CreateTemp(folder, "member-*.deflate")
	if err != nil {
		result.err = err
		return result
	}
	result.path = output.Name()
	defer func() {
		if err := output.Close(); result.err == nil {
			result.err = err
		}
	}()
	buffered := bufio.NewWriterSize(output, 64*1024)
	compressor, err := flate.NewWriter(buffered, level)
	if err != nil {
		result.err = err
		return result
	}
	digest, crc := sha256.New(), crc32.NewIEEE()
	count, err := io.CopyBuffer(io.MultiWriter(compressor, digest, crc), contextReader{ctx, source}, buffer)
	closeErr := compressor.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = buffered.Flush()
	}
	if err != nil {
		result.err = err
		return result
	}
	info, err := output.Stat()
	if err != nil {
		result.err = err
		return result
	}
	result.info.Size = uint64(count)
	result.info.CompressedSize = uint64(info.Size())
	result.info.CRC32 = crc.Sum32()
	result.info.SHA256 = fmt.Sprintf("%x", digest.Sum(nil))
	return result
}

func memberHeader(info MemberInfo) *zip.FileHeader {
	header := &zip.FileHeader{
		Name: info.Name, Method: zip.Deflate,
		Flags: 0x800, CreatorVersion: 20, ReaderVersion: 20,
		ModifiedDate: 33, // 1980-01-01, deterministic across workers and source mtimes.
		CRC32:        info.CRC32, CompressedSize64: info.CompressedSize, UncompressedSize64: info.Size,
	}
	header.SetMode(0644)
	return header
}

func appendMember(ctx context.Context, writer *zip.Writer, member compressedMember, buffer []byte) error {
	raw, err := os.Open(member.path)
	if err != nil {
		return err
	}
	defer raw.Close()
	destination, err := writer.CreateRaw(memberHeader(member.info))
	if err != nil {
		return err
	}
	count, err := io.CopyBuffer(destination, contextReader{ctx, raw}, buffer)
	if err == nil && uint64(count) != member.info.CompressedSize {
		err = fmt.Errorf("compressed member size changed")
	}
	return err
}

func validateInputs(files []File, options Options) error {
	if options.Level < flate.HuffmanOnly || options.Level > flate.BestCompression {
		return fmt.Errorf("invalid DEFLATE level %d", options.Level)
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if !fs.ValidPath(file.Name) || file.Name == "." || !utf8.ValidString(file.Name) || strings.ContainsAny(file.Name, "\\:\x00") || len(file.Name) > 65535 {
			return fmt.Errorf("invalid ZIP member name %q", file.Name)
		}
		if seen[file.Name] {
			return fmt.Errorf("duplicate ZIP member %q", file.Name)
		}
		seen[file.Name] = true
		if file.Path != "" && file.Data != nil {
			return fmt.Errorf("ZIP member %q specifies both Path and Data", file.Name)
		}
	}
	return nil
}

func assemble(ctx context.Context, files []File, destination io.Writer, folder string, options Options) ([]MemberInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	workers := options.Workers
	if workers <= 0 {
		workers = 2
	}
	workers = min(workers, 2, len(files))
	type job struct {
		file File
		done chan compressedMember
	}
	jobs := make(chan job, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			buffer := make([]byte, 1024*1024)
			for job := range jobs {
				job.done <- compressMember(ctx, job.file, folder, options.Level, buffer)
			}
		}()
	}
	defer func() {
		cancel()
		close(jobs)
		group.Wait()
	}()
	// Reuse a window of at most two result slots. A new job is issued only
	// after a previous member is assembled and its temporary file removed.
	pending := make([]chan compressedMember, workers)
	for i := range workers {
		pending[i] = make(chan compressedMember, 1)
		jobs <- job{files[i], pending[i]}
	}
	writer := zip.NewWriter(destination)
	buffer := make([]byte, 1024*1024)
	infos := make([]MemberInfo, 0, len(files))
	for i := range files {
		var member compressedMember
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case member = <-pending[i%workers]:
		}
		if member.err != nil {
			return nil, fmt.Errorf("compress %q: %w", files[i].Name, member.err)
		}
		if err := appendMember(ctx, writer, member, buffer); err != nil {
			return nil, fmt.Errorf("write %q: %w", member.info.Name, err)
		}
		if err := os.Remove(member.path); err != nil {
			return nil, err
		}
		infos = append(infos, member.info)
		if options.Progress != nil {
			options.Progress(Progress{Done: i + 1, Total: len(files), Name: member.info.Name})
		}
		if next := i + workers; next < len(files) {
			jobs <- job{files[next], pending[i%workers]}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return infos, nil
}

// WriteZIP compresses unchanged payloads, assembles members in input order, then
// rereads every saved member to verify its size, CRC32 and SHA-256. It also hashes
// the final ZIP. Compression and verification stream files with bounded buffers.
// Target must not exist. Only this call's temporary and incomplete files are removed.
func WriteZIP(ctx context.Context, files []File, target string, options Options) (report Report, err error) {
	if err = validateInputs(files, options); err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	if options.WorkDir != "" {
		if err = os.MkdirAll(options.WorkDir, 0700); err != nil {
			return report, err
		}
	}
	folder, err := os.MkdirTemp(options.WorkDir, "hd2-zip-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(folder)
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	defer func() {
		if output != nil {
			_ = output.Close()
		}
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	infos, err := assemble(ctx, files, output, folder, options)
	if err != nil {
		return report, err
	}
	if err = output.Close(); err != nil {
		return report, err
	}
	output = nil
	return VerifyZIP(ctx, target, infos)
}

// VerifyZIP streams all decompressed members (letting archive/zip validate CRC)
// and hashes the saved ZIP. Non-nil expected must match every member, in order.
func VerifyZIP(ctx context.Context, path string, expected []MemberInfo) (Report, error) {
	var report Report
	if err := ctx.Err(); err != nil {
		return report, err
	}
	input, err := os.Open(path)
	if err != nil {
		return report, err
	}
	defer input.Close()
	stat, err := input.Stat()
	if err != nil {
		return report, err
	}
	reader, err := zip.NewReader(input, stat.Size())
	if err != nil {
		return report, err
	}
	if expected != nil && len(reader.File) != len(expected) {
		return report, fmt.Errorf("ZIP member count differs: got %d, want %d", len(reader.File), len(expected))
	}
	buffer := make([]byte, 1024*1024)
	report.Members = make([]MemberInfo, 0, len(reader.File))
	for i, file := range reader.File {
		member, err := file.Open()
		if err != nil {
			return report, err
		}
		digest, crc := sha256.New(), crc32.NewIEEE()
		count, err := io.CopyBuffer(io.MultiWriter(digest, crc), contextReader{ctx, member}, buffer)
		closeErr := member.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return report, fmt.Errorf("verify %q: %w", file.Name, err)
		}
		info := MemberInfo{Name: file.Name, Size: uint64(count), CompressedSize: file.CompressedSize64, CRC32: crc.Sum32(), SHA256: fmt.Sprintf("%x", digest.Sum(nil))}
		if info.Size != file.UncompressedSize64 || info.CRC32 != file.CRC32 {
			return report, fmt.Errorf("ZIP member %q failed size/CRC verification", file.Name)
		}
		if expected != nil && info != expected[i] {
			return report, fmt.Errorf("ZIP member %q differs from input", file.Name)
		}
		report.Members = append(report.Members, info)
	}
	digest := sha256.New()
	report.Size, err = io.CopyBuffer(digest, contextReader{ctx, input}, buffer)
	if err != nil {
		return report, err
	}
	if report.Size != stat.Size() {
		return report, fmt.Errorf("ZIP size changed during readback")
	}
	report.SHA256 = fmt.Sprintf("%x", digest.Sum(nil))
	return report, nil
}
