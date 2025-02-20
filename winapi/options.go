package winapi

import "regexp"

// Filter represents a window filtering predicate
type Filter func(*Window) bool

// WinNot creates a negated filter
func WinNot(f Filter) Filter {
	return func(w *Window) bool {
		return !f(w)
	}
}

// WinVisible filters by window visibility
func WinVisible(isVisible bool) Filter {
	return func(w *Window) bool {
		return w.IsVisible == isVisible
	}
}

// WinClass filters by window class name
func WinClass(className string) Filter {
	return func(w *Window) bool {
		return w.ClassName == className
	}
}

// WinMinSize filters by minimum window dimensions
func WinMinSize(width, height int32) Filter {
	return func(w *Window) bool {
		return (w.Rect.Right-w.Rect.Left) >= width &&
			(w.Rect.Bottom-w.Rect.Top) >= height
	}
}

// WinTitlePattern filters by window title regex pattern
func WinTitlePattern(pattern regexp.Regexp) Filter {
	return func(w *Window) bool {
		return pattern.MatchString(w.Title)
	}
}
