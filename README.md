# LLM-searxng
## Use instance of searxng to add web search to any LLM

LLM-searxng takes your prompt and feeds it to searxng, scrapes the top websites, parses html and pdfs, and then it gives it all to the LLM web interface of your choosing. It will then send the LLM response back at you, allowing full conversations with secure search + any LLM, all from the comfort of your terminal.

## Setup
### searxng
You will need an instance of searxng for the program to access. Make sure that it is configured to output json, and that its built-in limiter is turned off so that it doesn't automatically block programatic access.


### settings.yaml
Create a settings.yaml file with the following to configure the program:

```yaml
# Base URL for the LLM service
llm_url: ""

# Web UI selectors for interacting with LLM interface
selectors:
  input_box: ""
  submit_button: ""
  output_box: ""

searxng_url: "http://localhost"
```

### llm

You can try to use this on any LLM site, however some may notice it is a bot and limit usage. Use at your own risk. You'll need to inspect element to find the main input text box to submit messages, the submit button to send messages, and the output box to capture the message from the LLM. 

If you need to sign in to access the LLM web interface or do anything else in the site to make it work, use `ctrl-o` to open the browser in a non-headless mode.


