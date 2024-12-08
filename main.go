package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"llm-searxng/browser"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dslipak/pdf"
	"github.com/go-rod/rod"
	"golang.org/x/net/html"
)

// Settings represents the application configuration
type Settings struct {
	LLM_URL   string `yaml:"llm_url"`
	Selectors struct {
		InputBox     string `yaml:"input_box"`
		SubmitButton string `yaml:"submit_button"`
		OutputBox    string `yaml:"output_box"`
	} `yaml:"selectors"`
}

var settings Settings

// SearchResult represents a single result from the search engine
type SearchResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// SearchResponse represents the entire response from the local search engine
type SearchResponse struct {
	Query           string         `json:"query"`
	NumberOfResults int            `json:"number_of_results"`
	Results         []SearchResult `json:"results"`
}

// searchWeb queries the local search engine and returns the parsed search response
func searchWeb(query string) (*SearchResponse, error) {
	// Encode the query parameter
	encodedQuery := url.QueryEscape(query)

	searchURL := fmt.Sprintf("http://localhost/search?q=%s&format=json", encodedQuery)

	resp, err := http.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("error querying local search engine: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 response from search engine: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %v", err)
	}

	var searchResponse SearchResponse
	err = json.Unmarshal(body, &searchResponse)
	if err != nil {
		return nil, fmt.Errorf("error parsing JSON response: %v", err)
	}

	return &searchResponse, nil
}

func readPdf(path string) (string, error) {
	r, err := pdf.Open(path)
	// remember close file
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	b, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	buf.ReadFrom(b)
	return buf.String(), nil
}

func scrapePDF(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("error fetching PDF %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("non-200 status code while scraping %s: %d", url, resp.StatusCode)
	}

	// Save the PDF to a temporary file
	tmpFile, err := os.CreateTemp("", "pdf-*.pdf")
	if err != nil {
		return "", fmt.Errorf("error creating temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name()) // Clean up the temp file when done
	defer tmpFile.Close()

	// Copy the PDF content from the response to the temp file
	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		return "", fmt.Errorf("error saving PDF to temp file: %v", err)
	}

	// Close the file before reading it
	tmpFile.Close()

	// Now read the PDF from the temp file
	content, err := readPdf(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("error reading PDF content: %v", err)
	}

	return content, nil
}

// scrapeWebsite fetches and extracts meaningful text content from a given URL
func scrapeWebsite(url string) (string, error) {
	if strings.HasSuffix(url, ".pdf") {
		return scrapePDF(url)
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a new request with context
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Use http.DefaultClient.Do() instead of http.Get()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error fetching URL %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("non-200 status code while scraping %s: %d", url, resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error parsing HTML from %s: %v", url, err)
	}

	var content strings.Builder
	var extractText func(*html.Node)

	// skipNode determines if we should skip processing this node and its children
	skipNode := func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}

		// Skip script, style, nav, footer, and other non-content elements
		skipTags := map[string]bool{
			"script":   true,
			"style":    true,
			"nav":      true,
			"footer":   true,
			"header":   true,
			"noscript": true,
			"iframe":   true,
			"svg":      true,
			"path":     true,
		}
		return skipTags[n.Data]
	}

	extractText = func(n *html.Node) {
		if skipNode(n) {
			return
		}

		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				// Check if parent node is a meaningful content element
				parentTag := ""
				if n.Parent != nil {
					parentTag = n.Parent.Data
				}

				// Add text with simple formatting
				switch parentTag {
				case "h1", "h2", "h3", "h4", "h5", "h6":
					content.WriteString("\n" + text + "\n")
				case "p":
					content.WriteString(text + "\n")
				case "li":
					content.WriteString(text + "\n")
				default:
					// Only add text if it's long enough to be meaningful
					if len(text) > 20 {
						content.WriteString(text + "\n")
					}
				}
			}
		}

		// Process child nodes
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractText(c)
		}
	}

	extractText(doc)

	// Clean up the extracted content
	result := content.String()

	// Remove excessive newlines
	result = strings.ReplaceAll(result, "\n\n\n", "\n\n")

	// Remove any remaining HTML entities
	result = strings.ReplaceAll(result, "&nbsp;", " ")
	result = strings.ReplaceAll(result, "&amp;", "&")
	result = strings.ReplaceAll(result, "&lt;", "<")
	result = strings.ReplaceAll(result, "&gt;", ">")

	// Remove any lines that are just special characters or very short
	lines := strings.Split(result, "\n")
	var cleanedLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) > 0 && !strings.Contains(line, "{}") && !strings.Contains(line, "[]") {
			cleanedLines = append(cleanedLines, line)
		}
	}

	return strings.Join(cleanedLines, "\n"), nil
}

func promptGen(searchResponse SearchResponse) (string, error) {
	var combinedContent strings.Builder

	LLM_INSTRUCTIONS := `
Use the following data from the internet to generate an accurate and concise response to the user query.
Look through the data to extract relevant information and structure your response in a clear and informative manner.
Use the information to make the response related to the user query as much as possible, discard irrelevant information.
Do not say "Based on provided information" or "According to the data" in the response, respond as if you are an expert in the field.
Do not use any markdown formatting in your response. Use plain text only.

SCHEMA:

User query: <original query>

Title: <title of search result>
URL: <URL of search result>
Search Engine Summary: <summary of webpage from search engine>
Scraped Content: <content scraped from website>

DATA:


	`

	combinedContent.WriteString(LLM_INSTRUCTIONS)

	// Add the original query
	combinedContent.WriteString("User query: " + searchResponse.Query + "\n\n")

	// Add content from each search result (limited to top 3)
	maxResults := 3
	if len(searchResponse.Results) < maxResults {
		maxResults = len(searchResponse.Results)
	}

	for i := 0; i < maxResults; i++ {
		result := searchResponse.Results[i]
		combinedContent.WriteString("\n---\n")
		combinedContent.WriteString(fmt.Sprintf("Title: %s\n", result.Title))
		combinedContent.WriteString(fmt.Sprintf("URL: %s\n", result.URL))
		combinedContent.WriteString(fmt.Sprintf("Search Engine Summary:\n%s\n", result.Content))

		// Scrape and add content from the website
		scrapedContent, err := scrapeWebsite(result.URL)
		if err != nil {
			combinedContent.WriteString(fmt.Sprintf("Error scraping website: %v\n", err))
		} else {
			combinedContent.WriteString("\nScraped Content:\n")

			const MAX_CONTENT_LENGTH = 20000
			// Limit scraped content to prevent excessive output
			if len(scrapedContent) > MAX_CONTENT_LENGTH {
				scrapedContent = scrapedContent[:MAX_CONTENT_LENGTH] + "...\n[Content truncated]"
			}
			combinedContent.WriteString(scrapedContent)
		}
		combinedContent.WriteString("---\n")
	}

	return combinedContent.String(), nil
}

var program *tea.Program

func loadSettings() error {
	data, err := os.ReadFile("settings.yaml")
	if err != nil {
		return fmt.Errorf("error reading settings.yaml: %v", err)
	}

	err = yaml.Unmarshal(data, &settings)
	if err != nil {
		return fmt.Errorf("error parsing settings.yaml: %v", err)
	}

	return nil
}

func main() {
	if err := loadSettings(); err != nil {
		fmt.Printf("Error loading settings: %v\n", err)
		os.Exit(1)
	}
	program = tea.NewProgram(initialModel())

	if _, err := program.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}

}

func submitQuery(llmInput string) error {
	// Create a context with timeout for the entire operation
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bm := browser.NewBrowserManager()
	bmErr := bm.Start(true)
	if bmErr != nil {
		return fmt.Errorf("failed to start browser: %v", bmErr)
	}

	// Create a cleanup function to ensure browser is stopped
	cleanupDone := make(chan struct{})
	cleanupOnce := make(chan struct{}, 1) // Buffer of 1 to prevent blocking
	defer func() {
		select {
		case cleanupOnce <- struct{}{}: // Only cleanup once
			bm.Stop()
			close(cleanupDone)
		default:
			// Cleanup already happened
		}
	}()

	// Navigate to either the saved chat URL or start a new chat
	var page *rod.Page
	var navErr error

	if chatURL != "" {
		page, navErr = bm.Navigate(chatURL)
	} else {
		page, navErr = bm.Navigate(settings.LLM_URL)
	}

	if navErr != nil {
		return fmt.Errorf("failed to navigate: %v", navErr)
	}

	llmInputBoxSelector := settings.Selectors.InputBox
	llmSubmitButtonSelector := settings.Selectors.SubmitButton
	llmOutputSelector := settings.Selectors.OutputBox

	// Wait for textarea to be present
	err := page.Timeout(20 * time.Second).MustElement(llmInputBoxSelector).WaitVisible()
	if err != nil {
		return fmt.Errorf("failed to find input box: %v", err)
	}

	// Add a small delay before typing
	time.Sleep(1 * time.Second)

	err = bm.InputText(page, llmInputBoxSelector, llmInput)
	if err != nil {
		return fmt.Errorf("failed to input text: %v", err)
	}

	err = page.MustElement(llmSubmitButtonSelector).WaitVisible()
	if err != nil {
		log.Fatal("Failed to find submit button:", err)
	}

	err = bm.ClickButton(page, llmSubmitButtonSelector)
	if err != nil {
		log.Fatal("Failed to click button:", err)
	}

	// Wait for output to be present
	err = page.MustElement(llmOutputSelector).WaitVisible()
	if err != nil {
		log.Fatal("Failed to find output box:", err)
	}

	updates, err := bm.ObserveSelector(ctx, page, llmOutputSelector)
	if err != nil {
		return fmt.Errorf("failed to observe selector: %v", err)
	}

	// Create a channel to signal when the goroutine is done
	finished := make(chan struct{})

	// Create a cleanup function to ensure we properly close everything
	cleanup := func() {
		select {
		case cleanupOnce <- struct{}{}: // Only cleanup once
			if chatURL == "" {
				sleepTime := 2 * time.Second
				time.Sleep(sleepTime)
				currentURL := page.MustInfo().URL
				if (currentURL != settings.LLM_URL) && (currentURL != "about:blank") {
					chatURL = currentURL
				}
			}
			cancel() // Cancel the context first

			// Wait for cleanup to complete with timeout
			select {
			case <-cleanupDone:
			case <-time.After(5 * time.Second):
				// Force cleanup if it takes too long
				bm.Stop()
			}
		default:
			// Cleanup already happened
		}
	}
	defer cleanup()

	// Track if there have been any changes
	lastUpdate := time.Now()
	noUpdateTimeout := 3 * time.Second

	// noResponseTimeout := 15 * time.Second

	timeoutTimer := time.NewTimer(noUpdateTimeout)
	defer timeoutTimer.Stop()

	go func() {
		isOutputting := false
		defer func() {
			if r := recover(); r != nil {
				// If we panic, ensure we set the state back to idle
				program.Send(loadingMsg{state: stateIdle, loading: false})
				cleanup()
			}
			close(finished)
		}()

		var lastText string
		currentState := stateIdle

		for {
			select {
			case text, ok := <-updates:
				if !ok || text == "" {
					if currentState != stateIdle {
						program.Send(loadingMsg{state: stateIdle, loading: false})
					}
					cleanup()
					return
				}

				// Only update if the text content has actually changed
				if text != lastText {
					if len(text) > 0 && !isOutputting {
						program.Send(loadingMsg{state: stateOutputting, loading: true})
						currentState = stateOutputting
						isOutputting = true
					}

					isOngoing := len(lastText) > 0
					program.Send(chatMsg{
						from:       ai,
						message:    text,
						ongoingMsg: isOngoing,
					})
					lastText = text
					lastUpdate = time.Now()
					timeoutTimer.Reset(noUpdateTimeout)
				}
			case <-timeoutTimer.C:
				if time.Since(lastUpdate) >= noUpdateTimeout {
					// If no new changes for noUpdateTimeout duration, finish up
					if currentState != stateIdle {
						program.Send(loadingMsg{state: stateIdle, loading: false})
						currentState = stateIdle
					}
					cleanup()
					return
				}
				timeoutTimer.Reset(noUpdateTimeout)
			}
		}
	}()

	// Wait for the output goroutine to finish before closing the browser
	<-finished
	return nil
}

type loadingState int

const (
	stateIdle loadingState = iota
	stateSearching
	stateGenerating
	stateSubmitting
	stateOutputting
)

type loadingMsg struct {
	state   loadingState
	loading bool
}

type from int

const (
	user from = iota
	ai
)

type chatMsg struct {
	from       from
	message    string
	ongoingMsg bool
}

type model struct {
	textarea       textarea.Model
	spinner        spinner.Model
	viewport       viewport.Model
	err            error
	loading        bool
	loadingState   loadingState
	loadingMessage string
	messages       []string
	viewportFocus  bool
	searchEnabled  bool // New field to track search state
}

var chatURL string = ""

func initialModel() model {
	ti := textarea.New()
	ti.FocusedStyle = textarea.Style{
		Base: lipgloss.NewStyle().Background(lipgloss.Color("black")),
	}
	ti.Placeholder = "Type your query here and press Ctrl+S to submit"
	ti.Focus()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E22E")) // Light green for spinner

	// Initialize viewport with flexible width
	vp := viewport.New(0, 30)
	vp.Style = lipgloss.NewStyle().
		PaddingLeft(2).
		PaddingRight(2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#666666")) // Darker gray for border
	vp.YPosition = 0

	return model{
		textarea:     ti,
		spinner:      s,
		viewport:     vp,
		err:          nil,
		loading:      false,
		loadingState: stateIdle,
		messages:     []string{},
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Account for document padding (2 on each side) and viewport padding/border
		contentWidth := msg.Width - 4 // Subtract document padding
		m.viewport.Width = contentWidth
		m.viewport.Height = msg.Height / 2 // Use half the window height for viewport
		m.textarea.SetWidth(contentWidth)
		return m, nil
	case spinner.TickMsg:
		if m.loading {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	case tea.KeyMsg:
		if m.viewportFocus {
			switch msg.Type {
			case tea.KeyEsc, tea.KeyTab:
				// Switch focus back to textarea
				m.viewportFocus = false
				return m, m.textarea.Focus()
			case tea.KeyUp:
				m.viewport.LineUp(1)
			case tea.KeyDown:
				m.viewport.LineDown(1)
			case tea.KeyRunes:
				switch string(msg.Runes) {
				case "k":
					m.viewport.LineUp(1)
				case "j":
					m.viewport.LineDown(1)
				}
			case tea.KeyPgUp:
				m.viewport.HalfViewUp()
			case tea.KeyPgDown:
				m.viewport.HalfViewDown()
			}
			return m, nil
		}

		switch msg.Type {
		case tea.KeyTab:
			// Switch focus between viewport and textarea
			if len(m.messages) > 0 {
				m.viewportFocus = !m.viewportFocus
				if m.viewportFocus {
					m.textarea.Blur()
				} else {
					return m, m.textarea.Focus()
				}
				return m, nil
			}
		case tea.KeyEsc:
			if m.textarea.Focused() {
				m.textarea.Blur()
			}
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyCtrlO:
			// Open browser for setup
			go func() {
				bm := browser.NewBrowserManager()
				if err := bm.Start(false); err != nil {
					program.Send(fmt.Errorf("failed to start browser: %v", err))
					return
				}
				defer bm.Stop()

				page, err := bm.Navigate(settings.LLM_URL)
				if err != nil {
					program.Send(fmt.Errorf("failed to navigate: %v", err))
					return
				}

				// Save the URL after successful navigation
				time.Sleep(2 * time.Second) // Give time for any redirects
				if currentURL := page.MustInfo().URL; currentURL != settings.LLM_URL {
					chatURL = currentURL
				}

				// Keep the browser open until user closes it
				<-page.Browser().GetContext().Done()
			}()
			return m, nil
		case tea.KeyCtrlF:
			m.searchEnabled = !m.searchEnabled
			return m, nil

		case tea.KeyCtrlS:
			if m.textarea.Value() != "" && m.err == nil && !m.loading {
				m.loading = true
				query := m.textarea.Value()

				if m.searchEnabled {
					m.loadingState = stateSearching
				} else {
					m.loadingState = stateSubmitting
				}

				userMsg := m.textarea.Value()
				m.textarea.Reset()

				// Create commands for sending messages
				sendUserMsg := func() tea.Msg {
					return chatMsg{
						from:       user,
						message:    userMsg,
						ongoingMsg: false,
					}
				}

				go func() {
					if m.searchEnabled {
						// First stage: Searching
						searchResponse, err := searchWeb(query)
						if err != nil {
							m.err = err
							program.Send(loadingMsg{state: stateIdle, loading: false})
							return
						}
						program.Send(loadingMsg{state: stateGenerating, loading: true})

						var result string
						if m.searchEnabled {
							// Second stage: Generating prompt
							result, err = promptGen(*searchResponse)
							if err != nil {
								m.err = err
								program.Send(loadingMsg{state: stateIdle, loading: false})
								return
							}
						} else {
							// Direct LLM interaction without search
							result = query
						}
						program.Send(loadingMsg{state: stateSubmitting, loading: true})

						// Third stage: Submitting to LLM
						err = submitQuery(result)
						if err != nil {
							m.err = err
							program.Send(loadingMsg{state: stateIdle, loading: false})
							return
						}
						// Don't send idle state here as submitQuery will handle it
					} else {
						// Direct LLM interaction without search
						err := submitQuery(query)
						if err != nil {
							m.err = err
							program.Send(loadingMsg{state: stateIdle, loading: false})
							return
						}
					}

				}()

				m.textarea.Placeholder = "Type new message here and press Ctrl+S to submit"
				return m, tea.Batch(sendUserMsg, tea.Batch(cmds...))
			}
		default:
			if !m.textarea.Focused() {
				cmd = m.textarea.Focus()
				cmds = append(cmds, cmd)
			}
		}

	case error:
		m.err = msg
		return m, nil
	case loadingMsg:
		m.loadingState = msg.state
		m.loading = msg.loading
		cmds = append(cmds, tea.Batch(textarea.Blink, m.spinner.Tick))
		return m, tea.Batch(cmds...)
	case chatMsg:
		// Account for viewport padding (2 on each side) and border (1 on each side)
		width := m.viewport.Width - 6

		userStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#AE81FF")). // Purple
			Width(width).
			PaddingLeft(1).
			PaddingRight(1).
			MarginBottom(1)

		aiStyleNormal := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#66D9EF")). // Light blue
			Width(width).
			PaddingLeft(1).
			PaddingRight(1).
			MarginBottom(1)

		aiStyleSearch := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FD971F")).
			Width(width).
			PaddingLeft(1).
			PaddingRight(1).
			MarginBottom(1)

		var formattedMsg string
		if msg.from == user {
			formattedMsg = "You: " + msg.message
			// Wrap the text first, then apply the style
			m.messages = append(m.messages, userStyle.Render(lipgloss.NewStyle().Width(width).Render(formattedMsg)))
		} else if msg.from == ai {
			formattedMsg = msg.message
			wrappedMsg := lipgloss.NewStyle().Width(width).Render(formattedMsg)
			var aiStyle lipgloss.Style
			if m.searchEnabled {
				aiStyle = aiStyleSearch
			} else {
				aiStyle = aiStyleNormal
			}
			if msg.ongoingMsg && len(m.messages) > 0 {
				m.messages[len(m.messages)-1] = aiStyle.Render(wrappedMsg)
			} else {
				m.messages = append(m.messages, aiStyle.Render(wrappedMsg))
			}
		}
		m.viewport.SetContent(strings.Join(m.messages, "\n"))
		m.viewport.GotoBottom()
	}

	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	if m.loading {
		cmds = append(cmds, m.spinner.Tick)
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	var s strings.Builder

	if m.loading {
		// s.Reset()
		loadingMessage := ""
		switch m.loadingState {
		case stateSearching:
			loadingMessage = "Searching the web"
		case stateGenerating:
			loadingMessage = "Generating prompt from search results"
		case stateSubmitting:
			loadingMessage = "Submitting to LLM for processing"
		case stateOutputting:
			loadingMessage = "The LLM is typing"
		}
		s.WriteString("\n\n" + loadingMessage + "... " + m.spinner.View() + "\n\n")
	}

	if len(m.messages) > 0 {
		s.WriteString(m.viewport.View() + "\n\n")
	}

	s.WriteString(m.textarea.View())

	s.WriteString("\n\n(")
	if len(m.messages) > 0 {
		s.WriteString("tab to switch focus • ")
		if m.viewportFocus {
			s.WriteString("j/k or ↑/↓ to scroll • ")
		}
	}
	searchStatus := "OFF"
	if m.searchEnabled {
		searchStatus = "ON"
	}
	s.WriteString(fmt.Sprintf("search: %s • ctrl+f to toggle • ", searchStatus))
	s.WriteString("ctrl+o to open browser • ")
	s.WriteString("ctrl+c to quit)")

	if m.err != nil {
		s.WriteString(fmt.Sprintf("\n\nError: %v", m.err))
	}

	docStyle := lipgloss.NewStyle().
		Padding(1, 2, 1, 2)

	return docStyle.Render(s.String()) + "\n"
}
