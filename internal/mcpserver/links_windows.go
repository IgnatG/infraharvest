// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package mcpserver

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// hardLinked reports whether the open file f has more than one name, a
// hard link to it: Windows' file info doesn't count them, so they are
// counted from its handle.
func hardLinked(f *os.File, _ fs.FileInfo) (bool, error) {
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &d); err != nil {
		return false, &fs.PathError{Op: "GetFileInformationByHandle", Path: f.Name(), Err: err}
	}
	return d.NumberOfLinks > 1, nil
}

// unfollowedLink reports whether the file at path, whose info from
// os.Lstat is info, is a link resolve doesn't follow: a junction, or
// another reparse point that names another file, which Go reports as
// fs.ModeIrregular since 1.23, rather than fs.ModeSymlink. Other reparse
// points Go reports so, such as a cloud file a sync client has yet to
// download, are files of their own.
func unfollowedLink(path string, info fs.FileInfo) (bool, error) {
	if info.Mode()&fs.ModeIrregular == 0 {
		return false, nil
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	// Lstat's info doesn't carry the reparse tag: the directory entry
	// does.
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(name, &data)
	if err != nil {
		return false, &fs.PathError{Op: "FindFirstFile", Path: path, Err: err}
	}
	_ = windows.FindClose(h)
	// IsReparseTagNameSurrogate: the tag's bit for a link to another
	// name, as a junction's IO_REPARSE_TAG_MOUNT_POINT has.
	const nameSurrogate = 0x20000000
	return data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && data.Reserved0&nameSurrogate != 0, nil
}
