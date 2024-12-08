package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type BrowserManager struct {
	browser *rod.Browser
	done    chan struct{}
}

func NewBrowserManager() *BrowserManager {
	return &BrowserManager{}
}

func (bm *BrowserManager) Start(headless bool) error {
	url := launcher.New().
		Headless(headless).
		Devtools(false).
		Set("ozone-platform", "wayland").
		Set("disable-blink-features", "AutomationControlled").
		Set("disable-web-security", "1").
		Set("no-sandbox", "1").
		Set("user-agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36").
		Set("user-data-dir", "/tmp/rod-profile").
		// Set("disable-restore-session-state", true). // Prevent session restore dialogs
		// Set("disable-component-update", true).      // Prevent auto-updates
		// Set("enable-features", "WebRTCPipeWireCapturer").
		MustLaunch()

	bm.browser = rod.New().
		ControlURL(url).
		// NoDefaultDevice().            // Allows manual interaction
		// SlowMotion(time.Millisecond). // Makes automated actions visible
		MustConnect()

	return nil
}

// WaitUntilClosed blocks until the browser window is closed
func (bm *BrowserManager) WaitUntilClosed() {
	if bm.browser != nil {
		<-bm.browser.GetContext().Done()
	}
}

func (bm *BrowserManager) Stop() {
	if bm.browser != nil {
		bm.browser.MustClose()
	}
}

func (bm *BrowserManager) Navigate(url string) (*rod.Page, error) {
	page := bm.browser.MustPage(url)
	err := page.WaitStable(2 * time.Second) // Wait for page to be stable
	if err != nil {
		return nil, fmt.Errorf("page failed to stabilize: %v", err)
	}
	return page, nil
}

// GetText gets text from an element matching the selector
func (bm *BrowserManager) GetText(page *rod.Page, selector string) (string, error) {
	try := page.MustWaitLoad().Timeout(5 * time.Second)
	el := try.MustElement(selector)
	if el == nil {
		return "", fmt.Errorf("element not found: %s", selector)
	}
	return el.Text()
}

// InputText types text into an element matching the selector
func (bm *BrowserManager) InputText(page *rod.Page, selector string, text string) error {
	try := page.MustWaitLoad().Timeout(5 * time.Second)
	el := try.MustElement(selector)
	if el == nil {
		return fmt.Errorf("element not found: %s", selector)
	}
	el.MustClick()                         // Focus the element
	el.MustSelectAllText().MustInput(text) // Clear and input new text
	return nil
}

// ClickButton clicks an element matching the selector
func (bm *BrowserManager) ClickButton(page *rod.Page, selector string) error {
	try := page.MustWaitLoad().Timeout(5 * time.Second)
	el := try.MustElement(selector)
	if el == nil {
		return fmt.Errorf("element not found: %s", selector)
	}
	return el.Click(proto.InputMouseButtonLeft, 1)
}

// StreamText continuously monitors a selector for text changes and sends updates through the channel
func (bm *BrowserManager) StreamText(page *rod.Page, selector string, updates chan<- string, done <-chan struct{}) error {
	// First wait for the element to exist
	el := page.MustElement(selector)
	if el == nil {
		return fmt.Errorf("element not found: %s", selector)
	}

	// Keep track of previous text to detect changes
	lastText := ""

	// Start monitoring loop
	ticker := time.NewTicker(100 * time.Millisecond) // Check every 100ms
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			// Get current text
			currentText, err := el.Text()
			if err != nil {
				continue // Skip this iteration if there's an error
			}

			// If text has changed, send update
			if currentText != lastText {
				select {
				case updates <- currentText:
					lastText = currentText
				case <-done:
					return nil
				}
			}
		}
	}
}

// ObserveSelector watches for changes to an element and returns updates through a channel
func (bm *BrowserManager) ObserveSelector(ctx context.Context, page *rod.Page, selector string) (<-chan string, error) {
	updates := make(chan string, 100) // Buffered channel to prevent blocking
	done := make(chan struct{})

	// Start the monitoring in a goroutine
	go func() {
		defer close(updates)

		// Create a new context that will be canceled when either ctx is canceled
		// or when we exit this function
		_, cancel := context.WithCancel(ctx)
		defer cancel()

		// Monitor for parent context cancellation
		go func() {
			<-ctx.Done()
			close(done)
		}()

		err := bm.StreamText(page, selector, updates, done)
		if err != nil {
			// In case of error, send empty string to signal completion
			select {
			case updates <- "":
			default:
			}
		}
	}()

	return updates, nil
}
