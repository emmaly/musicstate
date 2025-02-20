package winapi

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	// user32
	user32                   = syscall.NewLazyDLL("user32.dll")
	enumWindows              = user32.NewProc("EnumWindows")
	getClassName             = user32.NewProc("GetClassNameW")
	getWindowRect            = user32.NewProc("GetWindowRect")
	getWindowTextW           = user32.NewProc("GetWindowTextW")
	getWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	isWindowVisible          = user32.NewProc("IsWindowVisible")
	sendMessageW             = user32.NewProc("SendMessageW")

	// psapi
	psapi                = syscall.NewLazyDLL("psapi.dll")
	getModuleFileNameExW = psapi.NewProc("GetModuleFileNameExW")

	// kernel32
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	openProcess = kernel32.NewProc("OpenProcess")
)

var (
	enumCallback uintptr     // Store our callback
	windowsChan  chan Window // Channel for passing windows during enumeration
)

const (
	PROCESS_QUERY_INFORMATION = 0x0400
	PROCESS_VM_READ           = 0x0010
	WM_GETTEXTLENGTH          = 0x000E
)

// Window represents a Windows window with its associated metadata
type Window struct {
	Handle      uintptr
	Title       string
	ProcessName string
	ClassName   string // e.g. "Chrome_WidgetWin_1" for Electron apps
	IsVisible   bool
	Rect        WindowRect
}

type WindowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

func init() {
	// Create our callback once
	enumCallback = syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		if hwnd == 0 {
			return 1
		}

		var pid uint32
		getWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

		if name, err := getProcessExecutableName(pid); err == nil {
			w := Window{
				Handle:      hwnd,
				Title:       getWindowText(hwnd),
				ProcessName: name,
			}

			if err := w.Update(); err == nil {
				windowsChan <- w
			}
		}
		return 1
	})
}

// FindWindowsByProcess returns all windows belonging to the specified processes that pass the provided filters
func FindWindowsByProcess(processNames []string, filters ...Filter) ([]Window, error) {
	var windows []Window
	processMap := make(map[string]bool)
	for _, name := range processNames {
		processMap[strings.ToLower(name)] = true
	}

	cb := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		if hwnd == 0 {
			return 1
		}

		var pid uint32
		getWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

		if name, err := getProcessExecutableName(pid); err == nil &&
			processMap[strings.ToLower(name)] {

			w := Window{
				Handle:      hwnd,
				Title:       getWindowText(hwnd),
				ProcessName: name,
			}

			if err := w.Update(); err != nil {
				return 1
			}

			for _, filter := range filters {
				if !filter(&w) {
					return 1
				}
			}

			windows = append(windows, w)
		}
		return 1
	})

	enumWindows.Call(cb, 0)
	return windows, nil
}

// getWindowText retrieves the title text of a window
func getWindowText(hwnd uintptr) string {
	// Get text length first
	ret, _, _ := sendMessageW.Call(
		hwnd,
		WM_GETTEXTLENGTH,
		0,
		0,
	)
	length := int(ret)
	if length == 0 {
		return ""
	}

	// Allocate buffer for text (+1 for null terminator)
	buf := make([]uint16, length+1)

	// Get window text
	getWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(length+1),
	)

	return syscall.UTF16ToString(buf)
}

// getProcessExecutableName returns the executable name for a process ID
func getProcessExecutableName(pid uint32) (string, error) {
	handle, _, _ := openProcess.Call(
		PROCESS_QUERY_INFORMATION|PROCESS_VM_READ,
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return "", fmt.Errorf("could not open process %d", pid)
	}
	defer syscall.CloseHandle(syscall.Handle(handle))

	var buf [syscall.MAX_PATH]uint16
	ret, _, _ := getModuleFileNameExW.Call(
		handle,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return "", fmt.Errorf("could not get module filename")
	}

	// Extract just the executable name from the full path
	fullPath := syscall.UTF16ToString(buf[:])
	return filepath.Base(fullPath), nil
}

// getWindowRect retrieves the window dimensions
func (w *Window) updateRect() error {
	var rect WindowRect
	ret, _, err := getWindowRect.Call(
		w.Handle,
		uintptr(unsafe.Pointer(&rect)),
	)
	if ret == 0 {
		return fmt.Errorf("GetWindowRect failed: %v", err)
	}
	w.Rect = rect
	return nil
}

// updateVisibility checks if window is visible
func (w *Window) updateVisibility() error {
	ret, _, _ := isWindowVisible.Call(w.Handle)
	w.IsVisible = ret != 0
	return nil
}

// updateClassName gets the window class name
func (w *Window) updateClassName() error {
	var buf [256]uint16
	ret, _, err := getClassName.Call(
		w.Handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return fmt.Errorf("GetClassName failed: %v", err)
	}
	w.ClassName = syscall.UTF16ToString(buf[:])
	return nil
}

// Update refreshes all window state
func (w *Window) Update() error {
	if err := w.updateRect(); err != nil {
		return fmt.Errorf("updating rect: %w", err)
	}
	if err := w.updateVisibility(); err != nil {
		return fmt.Errorf("updating visibility: %w", err)
	}
	if err := w.updateClassName(); err != nil {
		return fmt.Errorf("updating class name: %w", err)
	}
	return nil
}
