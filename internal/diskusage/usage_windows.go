//go:build windows

package diskusage

import (
	"syscall"
	"unsafe"
)

// kernel32 / getDiskFreeSpaceEx are resolved LAZILY at first use.
//
// The stdlib `syscall` package has no GetDiskFreeSpaceEx wrapper on Windows —
// only golang.org/x/sys/windows does, and pulling that in would promote x/sys to
// a direct dependency and invalidate the flake's vendorHash (see the package
// doc). syscall.NewLazyDLL is the stdlib route to the same call and needs no cgo,
// so it builds under the release's CGO_ENABLED=0.
//
// NewLazyDLL does not touch the disk until the proc is called, so a
// mis-resolution surfaces as an error from Call, not a panic at package init.
var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// stat answers from GetDiskFreeSpaceExW.
//
//	BOOL GetDiskFreeSpaceExW(
//	  LPCWSTR         lpDirectoryName,
//	  PULARGE_INTEGER lpFreeBytesAvailableToCaller,
//	  PULARGE_INTEGER lpTotalNumberOfBytes,
//	  PULARGE_INTEGER lpTotalNumberOfFreeBytes);
//
// FreeBytesAvailableToCaller honours per-user disk quotas, so it is the Windows
// analogue of statfs' Bavail and maps to Usage.Free; TotalNumberOfFreeBytes is
// the quota-free figure and maps to the unallocated count Used is derived from.
// That keeps the meaning of the three fields identical on both platforms.
func stat(path string) (Usage, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var availToCaller, total, totalFree uint64
	// Call's first return is the BOOL; a zero BOOL means failure and only then is
	// the third return (the errno) meaningful.
	r, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&availToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		if callErr != nil {
			return Usage{}, callErr
		}
		return Usage{}, ErrUnsupported
	}
	return fromByteCounts(total, totalFree, availToCaller), nil
}
