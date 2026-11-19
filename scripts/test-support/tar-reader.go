// The independent tar reader checks archive bytes in the JavaScript tests.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "header":
		header, err := tar.NewReader(os.Stdin).Next()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode([3]string{
			header.Name,
			strconv.FormatInt(header.Size, 10),
			strconv.FormatInt(header.ModTime.UnixNano(), 10),
		})
	case len(args) == 3 && args[0] == "extract":
		count, err := extractArchive(args[1], args[2])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, count)
		return err
	default:
		return errors.New("expected header or extract with archive and destination paths")
	}
}

func extractArchive(archivePath, destination string) (int, error) {
	input, err := os.Open(archivePath)
	if err != nil {
		return 0, err
	}
	defer input.Close()

	compressed, err := gzip.NewReader(input)
	if err != nil {
		return 0, err
	}
	defer compressed.Close()

	root, err := filepath.Abs(destination)
	if err != nil {
		return 0, err
	}
	archive := tar.NewReader(compressed)
	count := 0
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if err := extractEntry(archive, root, header); err != nil {
			return 0, err
		}
		count++
	}
	// The gzip trailer must validate even if tar ended before it.
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return 0, err
	}
	return count, nil
}

func extractEntry(archive *tar.Reader, root string, header *tar.Header) error {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return errors.New("the reference reader accepts regular files only")
	}
	target := filepath.Join(root, filepath.FromSlash(header.Name))
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(header.Name) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("unsafe archive path %q", header.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(output, archive)
	if err := errors.Join(copyErr, output.Close()); err != nil {
		return err
	}
	if size != header.Size {
		return errors.New("incorrect file size")
	}
	return nil
}
