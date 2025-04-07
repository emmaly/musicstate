package winapi

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
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
	enumCallback uintptr              // Store our callback
	windowsChan  chan Window          // Channel for passing windows during enumeration
	callbackMap  = make(map[uintptr]bool) // Track active callbacks to prevent GC
	callbackMu   sync.Mutex           // Protects the callback map
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
	// Initialize windowsChan to prevent nil pointer panics
	windowsChan = make(chan Window, 100)
	
	// Create our callback once and store it to prevent garbage collection
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
				// Non-blocking send to channel
				select {
				case windowsChan <- w:
				default:
					// Channel is full or closed, skip this window
				}
			}
		}
		return 1
	})
	
	// Store the callback in our map to prevent garbage collection
	callbackMu.Lock()
	callbackMap[enumCallback] = true
	callbackMu.Unlock()
}

// FindWindowsByProcess returns all windows belonging to the specified processes that pass the provided filters
func FindWindowsByProcess(processNames []string, filters ...Filter) ([]Window, error) {
	var windows []Window
	processMap := make(map[string]bool)
	for _, name := range processNames {
		processMap[strings.ToLower(name)] = true
	}

	// Use a static callback to avoid GC issues with callback function pointers
	var done uintptr
	windowsChan = make(chan Window, 50) // Buffer to prevent blocking
	
	// Start a goroutine to collect windows from the channel
	go func() {
		for w := range windowsChan {
			matchesFilters := true
			for _, filter := range filters {
				if !filter(&w) {
					matchesFilters = false
					break
				}
			}
			
			if matchesFilters {
				windows = append(windows, w)
			}
		}
		done = 1
	}()
	
	// Create a filtered callback that only processes our target applications
	callback := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
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

			if err := w.Update(); err == nil {
				select {
				case windowsChan <- w:
				default:
					// Channel is full, skip this window
				}
			}
		}
		return 1
	})
	
	// Store the callback in our map to prevent garbage collection
	callbackMu.Lock()
	callbackMap[callback] = true
	callbackMu.Unlock()
	
	// Use our new callback
	enumWindows.Call(callback, 0)
	
	// Remove the callback from our map after enumeration completes
	defer func() {
		callbackMu.Lock()
		delete(callbackMap, callback)
		callbackMu.Unlock()
	}()
	
	// Close the channel and wait for collection to complete
	close(windowsChan)
	for done == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	
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
	
	// Safety cap for extremely large window titles to prevent allocation issues
	// Windows typically doesn't have window titles longer than 1000 chars
	if length > 4096 {
		length = 4096
	}

	// Allocate buffer for text (+1 for null terminator)
	buf := make([]uint16, length+1)

	// Get window text
	result, _, _ := getWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(length+1),
	)
	
	// If result is 0, the call failed
	if result == 0 {
		return ""
	}

	// Only convert the valid portion of the buffer
	actualLength := int(result)
	if actualLength > 0 && actualLength <= length {
		return syscall.UTF16ToString(buf[:actualLength])
	}
	
	// Fallback to converting the whole buffer (minus null terminator)
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

// updateRect retrieves the window dimensions
func (w *Window) updateRect() error {
	// Check if the handle is valid
	if w.Handle == 0 {
		return fmt.Errorf("invalid window handle")
	}
	
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
	// Check if the handle is valid
	if w.Handle == 0 {
		return fmt.Errorf("invalid window handle")
	}
	
	ret, _, _ := isWindowVisible.Call(w.Handle)
	w.IsVisible = ret != 0
	return nil
}

// updateClassName gets the window class name
func (w *Window) updateClassName() error {
	// Check if the handle is valid
	if w.Handle == 0 {
		return fmt.Errorf("invalid window handle")
	}
	
	var buf [256]uint16
	ret, _, err := getClassName.Call(
		w.Handle,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if ret == 0 {
		return fmt.Errorf("GetClassName failed: %v", err)
	}
	
	// Only use the valid portion of the buffer (up to the returned length)
	if ret > 0 && ret <= 256 {
		w.ClassName = syscall.UTF16ToString(buf[:ret])
	} else {
		w.ClassName = syscall.UTF16ToString(buf[:])
	}
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
