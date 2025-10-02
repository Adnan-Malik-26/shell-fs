package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		printPrompt()
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading input:", err)
			continue
		}

		if err = execInput(input); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
	}
}

func printPrompt() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Print("> ")
		return
	}

	// Show home directory as ~
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}

	// Get username for prompt
	currentUser, err := user.Current()
	username := "user"
	if err == nil {
		username = currentUser.Username
	}

	// Color prompt (green for user, blue for directory)
	fmt.Printf("\033[32m%s\033[0m:\033[34m%s\033[0m$ ", username, filepath.Base(cwd))
}

var ErrNoPath = errors.New("path required")

func execInput(input string) error {
	input = strings.TrimSpace(input)

	// Ignore empty input and comments
	if input == "" || strings.HasPrefix(input, "#") {
		return nil
	}

	// Parse the command and arguments
	args := parseInput(input)
	if len(args) == 0 {
		return nil
	}

	// Handle built-in commands
	switch args[0] {
	case "cd":
		return handleCD(args)
	case "exit":
		os.Exit(0)
	case "pwd":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		fmt.Println(cwd)
		return nil
	case "export":
		return handleExport(args)
	case "echo":
		fmt.Println(strings.Join(args[1:], " "))
		return nil
	}

	// Execute external command
	return execExternal(args)
}

func parseInput(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range input {
		switch {
		case r == '"' || r == '\'':
			if inQuote {
				if r == quoteChar {
					inQuote = false
					quoteChar = 0
				} else {
					current.WriteRune(r)
				}
			} else {
				inQuote = true
				quoteChar = r
			}
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, os.ExpandEnv(current.String()))
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		args = append(args, os.ExpandEnv(current.String()))
	}

	return args
}

func handleCD(args []string) error {
	var dir string

	if len(args) < 2 {
		// No argument: go to home directory
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not find home directory: %w", err)
		}
		dir = home
	} else {
		dir = args[1]

		// Expand ~ to home directory
		if strings.HasPrefix(dir, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("could not find home directory: %w", err)
			}
			dir = filepath.Join(home, dir[1:])
		}
	}

	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("cd: %w", err)
	}

	return nil
}

func handleExport(args []string) error {
	if len(args) < 2 {
		return errors.New("export: usage: export VAR=value")
	}

	for _, arg := range args[1:] {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("export: invalid format: %s", arg)
		}
		os.Setenv(parts[0], parts[1])
	}

	return nil
}

func execExternal(args []string) error {
	// Check if command exists
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("%s: command not found", args[0])
	}

	cmd := exec.Command(path, args[1:]...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
