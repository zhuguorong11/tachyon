package tachyon

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/flynn/go-shlex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

func captureCmd(c *exec.Cmd, show bool) (string, string, error) {
	stdout, err := c.StdoutPipe()

	if err != nil {
		return "", "", err
	}

	defer stdout.Close()

	var wg sync.WaitGroup

	var bout bytes.Buffer
	var berr bytes.Buffer

	prefix := []byte(`| `)

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := bufio.NewReader(stdout)

		for {
			line, err := buf.ReadSlice('\n')

			if err != nil {
				break
			}

			bout.Write(line)

			if show {
				os.Stdout.Write(prefix)
				os.Stdout.Write(line)
			}
		}
	}()

	stderr, err := c.StderrPipe()

	if err != nil {
		stdout.Close()
		return "", "", err
	}

	defer stderr.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := bufio.NewReader(stderr)

		for {
			line, err := buf.ReadSlice('\n')

			if err != nil {
				break
			}

			berr.Write(line)

			if show {
				os.Stdout.Write(prefix)
				os.Stdout.Write(line)
			}
		}
	}()

	c.Start()

	wg.Wait()

	err = c.Wait()

	return bout.String(), berr.String(), err
}

func runCmd(env *CommandEnv, parts []string) (*Result, error) {
	c := exec.Command(parts[0], parts[1:]...)

	rc := 0

	stdout, stderr, err := captureCmd(c, env.Env.config.ShowCommandOutput)
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			rc = 1
		} else {
			return nil, err
		}
	}

	r := NewResult(true)

	r.Add("rc", rc)
	r.Add("stdout", strings.TrimSpace(stdout))
	r.Add("stderr", strings.TrimSpace(stderr))

	return r, nil
}

type CommandCmd struct{}

func (cmd *CommandCmd) Run(env *CommandEnv, args string) (*Result, error) {
	parts, err := shlex.Split(args)

	if err != nil {
		return nil, err
	}

	return runCmd(env, parts)
}

type ShellCmd struct{}

func (cmd *ShellCmd) Run(env *CommandEnv, args string) (*Result, error) {
	return runCmd(env, []string{"sh", "-c", args})
}

type CopyCmd struct {
	Src  string `tachyon:"src,required"`
	Dest string `tachyon:"dest,required"`
}

func md5file(path string) ([]byte, error) {
	h := md5.New()

	i, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	if _, err := io.Copy(h, i); err != nil {
		return nil, err
	}

	return h.Sum(nil), nil
}

func (cmd *CopyCmd) Run(env *CommandEnv, args string) (*Result, error) {
	input, err := os.Open(cmd.Src)

	if err != nil {
		return nil, err
	}

	srcStat, err := os.Stat(cmd.Src)
	if err != nil {
		return nil, err
	}

	srcDigest, err := md5file(cmd.Src)
	if err != nil {
		return nil, err
	}

	var dstDigest []byte

	defer input.Close()

	dest := cmd.Dest

	link := false

	if stat, err := os.Lstat(dest); err == nil {
		if stat.IsDir() {
			dest = filepath.Join(dest, filepath.Base(cmd.Src))
		} else {
			dstDigest, _ = md5file(dest)
		}

		link = stat.Mode()&os.ModeSymlink != 0
	}

	rd := ResultData{
		"md5sum": Any(hex.Dump(srcDigest)),
		"src":    Any(cmd.Src),
		"dest":   Any(dest),
	}

	if dstDigest != nil && bytes.Equal(srcDigest, dstDigest) {
		return WrapResult(false, rd), nil
	}

	tmp := fmt.Sprintf("%s.tmp.%d", cmd.Dest, os.Getpid())

	output, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY, 0644)

	if err != nil {
		return nil, err
	}

	defer output.Close()

	if _, err = io.Copy(output, input); err != nil {
		os.Remove(tmp)
		return nil, err
	}

	if link {
		os.Remove(dest)
	}

	if err := os.Chmod(tmp, srcStat.Mode()); err != nil {
		os.Remove(tmp)
		return nil, err
	}

	if ostat, ok := srcStat.Sys().(*syscall.Stat_t); ok {
		os.Chown(tmp, int(ostat.Uid), int(ostat.Gid))
	}

	err = os.Rename(tmp, dest)
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}

	return WrapResult(true, rd), nil
}

type ScriptCmd struct{}

func (cmd *ScriptCmd) Run(env *CommandEnv, args string) (*Result, error) {
	script := args

	parts, err := shlex.Split(args)
	if err == nil {
		script = parts[0]
	}

	path := env.Paths.File(script)

	_, err = os.Stat(path)
	if err != nil {
		return nil, err
	}

	runArgs := append([]string{"sh", path}, parts[1:]...)

	return runCmd(env, runArgs)
}

func init() {
	RegisterCommand("command", &CommandCmd{})
	RegisterCommand("shell", &ShellCmd{})
	RegisterCommand("copy", &CopyCmd{})
	RegisterCommand("script", &ScriptCmd{})
}
