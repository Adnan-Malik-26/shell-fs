package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Job struct {
	ID      int
	PID     int
	Command string
	Stopped bool
}

var (
	jobs       = make(map[int]*Job)
	jobCounter = 1
	jobsMutex  sync.Mutex
	history    []string
	aliases    = make(map[string]string)
)

func main() {
	setupSignalHandlers()
	loadHistory()
	loadAliases()

	reader := bufio.NewReader(os.Stdin)
	for {
		printPrompt()
		input, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println("\nexit")
				break
			}
			fmt.Fprintln(os.Stderr, "Error reading input:", err)
			continue
		}

		input = strings.TrimSpace(input)
		if input != "" {
			history = append(history, input)
		}

		if err = execInput(input); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
	}

	saveHistory()
}

func setupSignalHandlers() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTSTP)

	go func() {
		for sig := range sigChan {
			switch sig {
			case syscall.SIGINT:
				fmt.Println("\n(Use 'exit' to quit)")
				printPrompt()
			case syscall.SIGTSTP:
				// Handle Ctrl+Z for job control
				fmt.Println("\n(Job stopped - use 'fg' to resume)")
			}
		}
	}()
}

func printPrompt() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Print("> ")
		return
	}

	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}

	currentUser, err := user.Current()
	username := "user"
	if err == nil {
		username = currentUser.Username
	}

	fmt.Printf("\033[32m%s\033[0m:\033[34m%s\033[0m$ ", username, filepath.Base(cwd))
}

func execInput(input string) error {
	input = strings.TrimSpace(input)

	if input == "" || strings.HasPrefix(input, "#") {
		return nil
	}

	// Check for background job
	background := strings.HasSuffix(input, "&")
	if background {
		input = strings.TrimSuffix(input, "&")
		input = strings.TrimSpace(input)
	}

	// Split by pipes
	commands := splitByPipes(input)

	if len(commands) == 1 {
		return execSingleCommand(commands[0], background)
	}

	return execPipeline(commands, background)
}

func splitByPipes(input string) []string {
	var commands []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)

	for _, r := range input {
		if r == '"' || r == '\'' {
			if inQuote && r == quoteChar {
				inQuote = false
			} else if !inQuote {
				inQuote = true
				quoteChar = r
			}
			current.WriteRune(r)
		} else if r == '|' && !inQuote {
			commands = append(commands, strings.TrimSpace(current.String()))
			current.Reset()
		} else {
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		commands = append(commands, strings.TrimSpace(current.String()))
	}

	return commands
}

func execSingleCommand(cmdStr string, background bool) error {
	args, inputFile, outputFile, appendMode, err := parseCommand(cmdStr)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	// Expand aliases
	if alias, ok := aliases[args[0]]; ok {
		aliasArgs := strings.Fields(alias)
		args = append(aliasArgs, args[1:]...)
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
		output := strings.Join(args[1:], " ")
		if outputFile != "" {
			return writeToFile(output, outputFile, appendMode)
		}
		fmt.Println(output)
		return nil
	case "history":
		return handleHistory(args)
	case "alias":
		return handleAlias(args)
	case "unalias":
		return handleUnalias(args)
	case "jobs":
		return handleJobs()
	case "fg":
		return handleFg(args)
	case "bg":
		return handleBg(args)
	}

	return execExternal(args, inputFile, outputFile, appendMode, background)
}

func parseCommand(cmdStr string) ([]string, string, string, bool, error) {
	var args []string
	var current strings.Builder
	var inputFile, outputFile string
	var appendMode bool
	inQuote := false
	quoteChar := rune(0)
	i := 0
	runes := []rune(cmdStr)

	for i < len(runes) {
		r := runes[i]

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
			i++
		case r == '<' && !inQuote:
			if current.Len() > 0 {
				args = append(args, os.ExpandEnv(current.String()))
				current.Reset()
			}
			i++
			for i < len(runes) && runes[i] == ' ' {
				i++
			}
			for i < len(runes) && runes[i] != ' ' {
				current.WriteRune(runes[i])
				i++
			}
			inputFile = current.String()
			current.Reset()
		case r == '>' && !inQuote:
			if current.Len() > 0 {
				args = append(args, os.ExpandEnv(current.String()))
				current.Reset()
			}
			i++
			if i < len(runes) && runes[i] == '>' {
				appendMode = true
				i++
			}
			for i < len(runes) && runes[i] == ' ' {
				i++
			}
			for i < len(runes) && runes[i] != ' ' {
				current.WriteRune(runes[i])
				i++
			}
			outputFile = current.String()
			current.Reset()
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, os.ExpandEnv(current.String()))
				current.Reset()
			}
			i++
		default:
			current.WriteRune(r)
			i++
		}
	}

	if current.Len() > 0 {
		args = append(args, os.ExpandEnv(current.String()))
	}

	return args, inputFile, outputFile, appendMode, nil
}

func execPipeline(commands []string, background bool) error {
	var cmds []*exec.Cmd

	for i, cmdStr := range commands {
		args, inputFile, outputFile, appendMode, err := parseCommand(cmdStr)
		if err != nil {
			return err
		}

		if len(args) == 0 {
			continue
		}

		path, err := exec.LookPath(args[0])
		if err != nil {
			return fmt.Errorf("%s: command not found", args[0])
		}

		cmd := exec.Command(path, args[1:]...)

		// Handle input redirection for first command
		if i == 0 && inputFile != "" {
			file, err := os.Open(inputFile)
			if err != nil {
				return err
			}
			cmd.Stdin = file
		}

		// Handle output redirection for last command
		if i == len(commands)-1 && outputFile != "" {
			var file *os.File
			var err error
			if appendMode {
				file, err = os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			} else {
				file, err = os.Create(outputFile)
			}
			if err != nil {
				return err
			}
			defer file.Close()
			cmd.Stdout = file
		}

		cmd.Stderr = os.Stderr
		cmds = append(cmds, cmd)
	}

	// Connect pipes
	for i := 0; i < len(cmds)-1; i++ {
		pipe, err := cmds[i].StdoutPipe()
		if err != nil {
			return err
		}
		cmds[i+1].Stdin = pipe
	}

	// Set stdout for last command if not redirected
	if cmds[len(cmds)-1].Stdout == nil {
		cmds[len(cmds)-1].Stdout = os.Stdout
	}

	// Set stdin for first command if not redirected
	if cmds[0].Stdin == nil {
		cmds[0].Stdin = os.Stdin
	}

	// Start all commands
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			return err
		}
	}

	if background {
		jobsMutex.Lock()
		job := &Job{
			ID:      jobCounter,
			PID:     cmds[len(cmds)-1].Process.Pid,
			Command: strings.Join(commands, " | "),
		}
		jobs[jobCounter] = job
		fmt.Printf("[%d] %d\n", job.ID, job.PID)
		jobCounter++
		jobsMutex.Unlock()

		go func() {
			for _, cmd := range cmds {
				cmd.Wait()
			}
		}()
		return nil
	}

	// Wait for all commands
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			return err
		}
	}

	return nil
}

func execExternal(args []string, inputFile, outputFile string, appendMode, background bool) error {
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("%s: command not found", args[0])
	}

	cmd := exec.Command(path, args[1:]...)

	// Handle input redirection
	if inputFile != "" {
		file, err := os.Open(inputFile)
		if err != nil {
			return err
		}
		defer file.Close()
		cmd.Stdin = file
	} else {
		cmd.Stdin = os.Stdin
	}

	// Handle output redirection
	if outputFile != "" {
		var file *os.File
		if appendMode {
			file, err = os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		} else {
			file, err = os.Create(outputFile)
		}
		if err != nil {
			return err
		}
		defer file.Close()
		cmd.Stdout = file
	} else {
		cmd.Stdout = os.Stdout
	}

	cmd.Stderr = os.Stderr

	if background {
		if err := cmd.Start(); err != nil {
			return err
		}

		jobsMutex.Lock()
		job := &Job{
			ID:      jobCounter,
			PID:     cmd.Process.Pid,
			Command: strings.Join(args, " "),
		}
		jobs[jobCounter] = job
		fmt.Printf("[%d] %d\n", job.ID, job.PID)
		jobCounter++
		jobsMutex.Unlock()

		go cmd.Wait()
		return nil
	}

	return cmd.Run()
}

func handleCD(args []string) error {
	var dir string

	if len(args) < 2 {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("could not find home directory: %w", err)
		}
		dir = home
	} else if args[1] == "-" {
		dir = os.Getenv("OLDPWD")
		if dir == "" {
			return errors.New("cd: OLDPWD not set")
		}
		fmt.Println(dir)
	} else {
		dir = args[1]

		if strings.HasPrefix(dir, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("could not find home directory: %w", err)
			}
			dir = filepath.Join(home, dir[1:])
		}
	}

	oldPwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("cd: %w", err)
	}
	os.Setenv("OLDPWD", oldPwd)

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

func handleHistory(args []string) error {
	count := len(history)
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("history: invalid number: %s", args[1])
		}
		if n < count {
			count = n
		}
	}

	start := len(history) - count
	if start < 0 {
		start = 0
	}

	for i := start; i < len(history); i++ {
		fmt.Printf("%4d  %s\n", i+1, history[i])
	}

	return nil
}

func handleAlias(args []string) error {
	if len(args) == 1 {
		for name, value := range aliases {
			fmt.Printf("alias %s='%s'\n", name, value)
		}
		return nil
	}

	for _, arg := range args[1:] {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("alias: invalid format: %s", arg)
		}
		aliases[parts[0]] = strings.Trim(parts[1], "'\"")
	}

	return nil
}

func handleUnalias(args []string) error {
	if len(args) < 2 {
		return errors.New("unalias: usage: unalias name")
	}

	for _, name := range args[1:] {
		delete(aliases, name)
	}

	return nil
}

func handleJobs() error {
	jobsMutex.Lock()
	defer jobsMutex.Unlock()

	for id, job := range jobs {
		status := "Running"
		if job.Stopped {
			status = "Stopped"
		}
		fmt.Printf("[%d]  %s\t%s\n", id, status, job.Command)
	}

	return nil
}

func handleFg(args []string) error {
	jobsMutex.Lock()
	defer jobsMutex.Unlock()

	if len(jobs) == 0 {
		return errors.New("fg: no jobs")
	}

	var jobID int
	if len(args) > 1 {
		id, err := strconv.Atoi(strings.TrimPrefix(args[1], "%"))
		if err != nil {
			return fmt.Errorf("fg: invalid job id: %s", args[1])
		}
		jobID = id
	} else {
		// Get most recent job
		maxID := 0
		for id := range jobs {
			if id > maxID {
				maxID = id
			}
		}
		jobID = maxID
	}

	job, ok := jobs[jobID]
	if !ok {
		return fmt.Errorf("fg: job %d not found", jobID)
	}

	fmt.Printf("%s\n", job.Command)
	// Note: Full job control requires more complex signal handling
	delete(jobs, jobID)

	return nil
}

func handleBg(args []string) error {
	return errors.New("bg: not fully implemented")
}

func writeToFile(content, filename string, appendMode bool) error {
	var file *os.File
	var err error

	if appendMode {
		file, err = os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	} else {
		file, err = os.Create(filename)
	}

	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(content + "\n")
	return err
}

func loadHistory() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	histFile := filepath.Join(home, ".gosh_history")
	file, err := os.Open(histFile)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		history = append(history, scanner.Text())
	}
}

func saveHistory() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	histFile := filepath.Join(home, ".gosh_history")
	file, err := os.Create(histFile)
	if err != nil {
		return
	}
	defer file.Close()

	// Save last 1000 commands
	start := 0
	if len(history) > 1000 {
		start = len(history) - 1000
	}

	for i := start; i < len(history); i++ {
		file.WriteString(history[i] + "\n")
	}
}

func loadAliases() {
	// Some default aliases
	aliases["ll"] = "ls -la"
	aliases["la"] = "ls -a"
	aliases[".."] = "cd .."
	aliases["..."] = "cd ../.."
}
