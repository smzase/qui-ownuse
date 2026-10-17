// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package update

import (
	"archive/tar"
	"compress/gzip"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const MaxUploadedBinarySize = 256 << 20

var (
	ErrUploadedBinaryUnsupported = errors.New("uploaded binary updates are only supported on Linux")
	ErrUploadedBinaryTooLarge    = errors.New("uploaded binary exceeds the size limit")
	ErrRestartPending            = errors.New("an update is already waiting to restart")
)

// BinaryInstaller replaces the currently running Linux executable with an uploaded binary.
type BinaryInstaller struct {
	mu             sync.Mutex
	restartPending bool
}

func NewBinaryInstaller() *BinaryInstaller {
	return &BinaryInstaller{}
}

func (i *BinaryInstaller) IsSupported() bool {
	return runtime.GOOS == "linux"
}

// Install copies and validates the uploaded binary before atomically replacing
// the current executable. The caller then requests a graceful restart.
func (i *BinaryInstaller) Install(source io.Reader) error {
	if !i.IsSupported() {
		return ErrUploadedBinaryUnsupported
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if i.restartPending {
		return ErrRestartPending
	}

	executablePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}

	executableInfo, err := os.Stat(executablePath)
	if err != nil {
		return fmt.Errorf("stat current executable: %w", err)
	}
	if !executableInfo.Mode().IsRegular() {
		return errors.New("current executable is not a regular file")
	}

	temporaryFile, err := os.CreateTemp(filepath.Dir(executablePath), ".qui-update-*")
	if err != nil {
		return fmt.Errorf("create update file: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	defer func() {
		_ = temporaryFile.Close()
		_ = os.Remove(temporaryPath)
	}()

	written, err := io.Copy(temporaryFile, io.LimitReader(source, MaxUploadedBinarySize+1))
	if err != nil {
		return fmt.Errorf("write update file: %w", err)
	}
	if written > MaxUploadedBinarySize {
		return ErrUploadedBinaryTooLarge
	}
	if written == 0 {
		return errors.New("uploaded binary is empty")
	}
	if err := temporaryFile.Chmod(executableInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("set update file permissions: %w", err)
	}
	if err := temporaryFile.Sync(); err != nil {
		return fmt.Errorf("sync update file: %w", err)
	}
	if err := temporaryFile.Close(); err != nil {
		return fmt.Errorf("close update file: %w", err)
	}

	binaryPath := temporaryPath
	if err := validateLinuxExecutable(binaryPath); err != nil {
		binaryPath, err = extractLinuxBinaryFromArchive(temporaryPath, filepath.Dir(executablePath), executableInfo.Mode().Perm())
		if err != nil {
			return fmt.Errorf("uploaded file must be a Linux executable or release archive: %w", err)
		}
		defer os.Remove(binaryPath)

		if err := validateLinuxExecutable(binaryPath); err != nil {
			return err
		}
	}
	if err := os.Rename(binaryPath, executablePath); err != nil {
		return fmt.Errorf("replace current executable: %w", err)
	}

	i.restartPending = true
	return nil
}

func extractLinuxBinaryFromArchive(archivePath, destinationDir string, mode os.FileMode) (string, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open release archive: %w", err)
	}
	defer archiveFile.Close()

	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return "", fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != "qui" || !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if header.Size <= 0 {
			return "", errors.New("release archive contains an empty qui binary")
		}
		if header.Size > MaxUploadedBinarySize {
			return "", ErrUploadedBinaryTooLarge
		}

		destination, err := os.CreateTemp(destinationDir, ".qui-update-archive-*")
		if err != nil {
			return "", fmt.Errorf("create extracted update file: %w", err)
		}
		destinationPath := destination.Name()
		keepDestination := false
		defer func() {
			_ = destination.Close()
			if !keepDestination {
				_ = os.Remove(destinationPath)
			}
		}()

		written, err := io.Copy(destination, io.LimitReader(tarReader, MaxUploadedBinarySize+1))
		if err != nil {
			return "", fmt.Errorf("extract binary from release archive: %w", err)
		}
		if written != header.Size {
			return "", errors.New("release archive ended before the qui binary was complete")
		}
		if err := destination.Chmod(mode); err != nil {
			return "", fmt.Errorf("set extracted update file permissions: %w", err)
		}
		if err := destination.Sync(); err != nil {
			return "", fmt.Errorf("sync extracted update file: %w", err)
		}
		if err := destination.Close(); err != nil {
			return "", fmt.Errorf("close extracted update file: %w", err)
		}

		keepDestination = true
		return destinationPath, nil
	}

	return "", errors.New("release archive does not contain a qui binary")
}

func validateLinuxExecutable(filePath string) error {
	executable, err := elf.Open(filePath)
	if err != nil {
		return fmt.Errorf("uploaded file is not an ELF executable: %w", err)
	}
	defer executable.Close()

	if executable.Type != elf.ET_EXEC && executable.Type != elf.ET_DYN {
		return errors.New("uploaded file is not an executable ELF file")
	}

	expectedMachine, ok := elfMachineForArchitecture(runtime.GOARCH)
	if !ok {
		return fmt.Errorf("unsupported Linux architecture: %s", runtime.GOARCH)
	}
	if executable.Machine != expectedMachine {
		return fmt.Errorf("uploaded binary targets %s, current server requires %s", executable.Machine, expectedMachine)
	}

	return nil
}

func elfMachineForArchitecture(architecture string) (elf.Machine, bool) {
	switch architecture {
	case "386":
		return elf.EM_386, true
	case "amd64":
		return elf.EM_X86_64, true
	case "arm":
		return elf.EM_ARM, true
	case "arm64":
		return elf.EM_AARCH64, true
	case "riscv64":
		return elf.EM_RISCV, true
	default:
		return 0, false
	}
}
