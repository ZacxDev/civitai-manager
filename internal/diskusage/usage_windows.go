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
// ⚠ RESOLUTION FAILURE IS A PANIC, NOT AN ERROR. NewLazyDLL does not touch the
// disk until the proc is called, so nothing can fail at package init — but the
// deferred failure does NOT come back as Call's error return. Go 1.25's
// syscall/dll_windows.go:
//
//	func (p *LazyProc) Call(a ...uintptr) (r1, r2 uintptr, lastErr error) {
//		p.mustFind()
//		...
//	}
//	func (p *LazyProc) mustFind() { if e := p.Find(); e != nil { panic(e) } }
//
// so an unresolvable kernel32.dll or GetDiskFreeSpaceExW panics inside whatever
// goroutine called Stat — here, an HTTP handler's. This is documented rather than
// recovered: kernel32 is a KnownDLL and GetDiskFreeSpaceExW has been exported
// since Windows 95, so the condition is not reachable on a supported system, and
// a recover() around it would be untestable-anywhere code that also swallows
// genuine bugs. See Stat's doc in usage.go, which states the same limit.
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
//
// THE OUT-PARAMS GO INTO A NAMED STRUCT rather than three bare locals, and the
// Usage mapping lives on that struct (diskFreeSpaceExOut.usage, in usage.go). The
// point is testability: this function cannot execute on any machine that builds
// this repo, so with the mapping inline nothing could catch a
// totalFree/availToCaller swap — which reports the quota-free figure as the
// user's free space, a wrong number rather than a visible failure. The field
// names also make the argument order below self-checking against the prototype
// above; TestWindowsShimWiresItsOutParams pins that order as source text.
func stat(path string) (Usage, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var out diskFreeSpaceExOut
	// Call's first return is the BOOL; a zero BOOL means failure and only then is
	// the third return (the errno) meaningful.
	r, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&out.AvailToCaller)),
		uintptr(unsafe.Pointer(&out.Total)),
		uintptr(unsafe.Pointer(&out.TotalFree)),
	)
	if r == 0 {
		if callErr != nil {
			return Usage{}, callErr
		}
		return Usage{}, ErrUnsupported
	}
	return out.usage(), nil
}
