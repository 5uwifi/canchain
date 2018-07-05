//
// (at your option) any later version.
//
//

package repl

import (
	"fmt"
	"strings"

	"github.com/peterh/liner"
)

var Stdin = newTerminalPrompter()

type UserPrompter interface {
	// PromptInput displays the given prompt to the user and requests some textual
	// data to be entered, returning the input of the user.
	PromptInput(prompt string) (string, error)

	// PromptPassword displays the given prompt to the user and requests some textual
	// data to be entered, but one which must not be echoed out into the terminal.
	// The method returns the input provided by the user.
	PromptPassword(prompt string) (string, error)

	// PromptConfirm displays the given prompt to the user and requests a boolean
	// choice to be made, returning that choice.
	PromptConfirm(prompt string) (bool, error)

	// SetHistory sets the the input scrollback history that the prompter will allow
	// the user to scroll back to.
	SetHistory(history []string)

	// AppendHistory appends an entry to the scrollback history. It should be called
	// if and only if the prompt to append was a valid command.
	AppendHistory(command string)

	// ClearHistory clears the entire history
	ClearHistory()

	// SetWordCompleter sets the completion function that the prompter will call to
	// fetch completion candidates when the user presses tab.
	SetWordCompleter(completer WordCompleter)
}

// "Word"}, "!!!") to have "Hello, world!!!".
type WordCompleter func(line string, pos int) (string, []string, string)

type terminalPrompter struct {
	*liner.State
	warned     bool
	supported  bool
	normalMode liner.ModeApplier
	rawMode    liner.ModeApplier
}

func newTerminalPrompter() *terminalPrompter {
	p := new(terminalPrompter)
	// Get the original mode before calling NewLiner.
	// This is usually regular "cooked" mode where characters echo.
	normalMode, _ := liner.TerminalMode()
	// Turn on liner. It switches to raw mode.
	p.State = liner.NewLiner()
	rawMode, err := liner.TerminalMode()
	if err != nil || !liner.TerminalSupported() {
		p.supported = false
	} else {
		p.supported = true
		p.normalMode = normalMode
		p.rawMode = rawMode
		// Switch back to normal mode while we're not prompting.
		normalMode.ApplyMode()
	}
	p.SetCtrlCAborts(true)
	p.SetTabCompletionStyle(liner.TabPrints)
	p.SetMultiLineMode(true)
	return p
}

func (p *terminalPrompter) PromptInput(prompt string) (string, error) {
	if p.supported {
		p.rawMode.ApplyMode()
		defer p.normalMode.ApplyMode()
	} else {
		// liner tries to be smart about printing the prompt
		// and doesn't print anything if input is redirected.
		// Un-smart it by printing the prompt always.
		fmt.Print(prompt)
		prompt = ""
		defer fmt.Println()
	}
	return p.State.Prompt(prompt)
}

func (p *terminalPrompter) PromptPassword(prompt string) (passwd string, err error) {
	if p.supported {
		p.rawMode.ApplyMode()
		defer p.normalMode.ApplyMode()
		return p.State.PasswordPrompt(prompt)
	}
	if !p.warned {
		fmt.Println("!! Unsupported terminal, password will be echoed.")
		p.warned = true
	}
	// Just as in Prompt, handle printing the prompt here instead of relying on liner.
	fmt.Print(prompt)
	passwd, err = p.State.Prompt("")
	fmt.Println()
	return passwd, err
}

func (p *terminalPrompter) PromptConfirm(prompt string) (bool, error) {
	input, err := p.Prompt(prompt + " [y/N] ")
	if len(input) > 0 && strings.ToUpper(input[:1]) == "Y" {
		return true, nil
	}
	return false, err
}

func (p *terminalPrompter) SetHistory(history []string) {
	p.State.ReadHistory(strings.NewReader(strings.Join(history, "\n")))
}

func (p *terminalPrompter) AppendHistory(command string) {
	p.State.AppendHistory(command)
}

func (p *terminalPrompter) ClearHistory() {
	p.State.ClearHistory()
}

func (p *terminalPrompter) SetWordCompleter(completer WordCompleter) {
	p.State.SetWordCompleter(liner.WordCompleter(completer))
}
