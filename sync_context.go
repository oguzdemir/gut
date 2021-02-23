package main

import (
	"crypto/md5"
	"errors"
	"fmt"
	"os/user"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/oguzdemir/bismuth"
)

type SyncContext struct {
	*bismuth.ExecContext
	syncPath        string
	hasGutInstalled *bool
	tailHash        string
}

var AllSyncContexts = []*SyncContext{}

func NewSyncContext() *SyncContext {
	ctx := &SyncContext{}
	ctx.ExecContext = bismuth.NewExecContext()
	AllSyncContexts = append(AllSyncContexts, ctx)
	return ctx
}

var remotePathRegexp = regexp.MustCompile("^((([^@]+)@)?([^:]+):)?(.+)$")

func (ctx *SyncContext) ParseSyncPath(path string) error {
	parts := remotePathRegexp.FindStringSubmatch(path)
	if len(parts) == 0 {
		return errors.New(fmt.Sprintf("Could not parse remote path: [%s]\n", path))
	}
	isRemote := len(parts[1]) > 0
	if isRemote {
		if len(parts[3]) > 0 {
			ctx.SetUsername(parts[3])
		} else {
			currUser, err := user.Current()
			if err == nil {
				ctx.SetUsername(currUser.Username)
			}
		}
		ctx.SetHostname(parts[4])
	}
	ctx.syncPath = parts[5]
	return nil
}

func (ctx *SyncContext) AbsSyncPath() string {
	return ctx.AbsPath(ctx.syncPath)
}

func (ctx *SyncContext) String() string {
	if ctx.Hostname() != "" {
		return fmt.Sprintf("{SyncContext %s@%s:%s}", ctx.Username(), ctx.Hostname(), ctx.syncPath)
	}
	return fmt.Sprintf("{SyncContext local %s}", ctx.syncPath)
}

func (ctx *SyncContext) BranchName() string {
	hostname := ctx.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}
	return fmt.Sprintf("%s-%s", hostname, fmt.Sprintf("%x", md5.Sum([]byte(ctx.String())))[:8])
}

func (ctx *SyncContext) PathAnsi(p string) string {
	if !ctx.IsLocal() {
		return fmt.Sprintf(ctx.Logger().Colorify("@(host:%s)@(dim:@)%s@(dim::)@(path:%s)"), ctx.Username(), ctx.NameAnsi(), p)
	}
	return fmt.Sprintf(ctx.Logger().Colorify("@(path:%s)"), p)
}

func (ctx *SyncContext) SyncPathAnsi() string {
	return ctx.PathAnsi(ctx.syncPath)
}

func (ctx *SyncContext) GutExe() string {
	return ctx.AbsPath(GutExePath)
}

func (ctx *SyncContext) ResetHasGutInstalled() {
	ctx.hasGutInstalled = nil
}

func (ctx *SyncContext) HasGutInstalled() bool {
	if ctx.hasGutInstalled == nil {
		hasGutInstalled := ctx._hasGutInstalled()
		ctx.hasGutInstalled = &hasGutInstalled
	}
	return *ctx.hasGutInstalled
}

func (ctx *SyncContext) _hasGutInstalled() bool {
	status := ctx.Logger()
	desiredGitVersion := GitVersion
	if ctx.IsWindows() {
		desiredGitVersion = GitWinVersion
	}
	exists, err := ctx.PathExists(GutExePath)
	if err != nil {
		status.Bail(err)
	}
	if exists {
		actualGutVersion, err := ctx.Output(ctx.AbsPath(GutExePath), "--version")
		if err != nil {
			status.Bail(err)
		}
		if strings.Contains(string(actualGutVersion), strings.TrimLeft(desiredGitVersion, "v")) {
			return true
		}
	}
	return false
}

func (ctx *SyncContext) GetTailHash() string {
	return ctx.tailHash
}

// Query the gut repo for the initial commit to the repo. We use this to determine if two gut repos are compatibile.
// http://stackoverflow.com/questions/1006775/how-to-reference-the-initial-commit
func (ctx *SyncContext) UpdateTailHash() {
	exists, err := ctx.PathExists(path.Join(ctx.AbsSyncPath(), ".gut"))
	if err != nil {
		ctx.Logger().Bail(err)
	}
	if exists {
		output, err := ctx.GutOutput("rev-list", "--max-parents=0", "HEAD")
		if err != nil {
			ctx.Logger().Bail(err)
		}
		ctx.tailHash = strings.TrimSpace(output)
	} else {
		ctx.tailHash = ""
	}
}

func (ctx *SyncContext) GutArgs(otherArgs ...string) []string {
	args := []string{}
	args = append(args, ctx.GutExe())
	return append(args, otherArgs...)
}

func (ctx *SyncContext) GutRun(args ...string) ([]byte, []byte, int, error) {
	return ctx.RunCwd(ctx.AbsSyncPath(), ctx.GutArgs(args...)...)
}

func (ctx *SyncContext) GutOutput(args ...string) (string, error) {
	return ctx.OutputCwd(ctx.AbsSyncPath(), ctx.GutArgs(args...)...)
}

func (ctx *SyncContext) GutQuoteBuf(suffix string, args ...string) (stdout []byte, stderr []byte, retCode int, err error) {
	return ctx.QuoteCwdBuf(suffix, ctx.AbsSyncPath(), ctx.GutArgs(args...)...)
}

func (ctx *SyncContext) GutQuote(suffix string, args ...string) (int, error) {
	return ctx.QuoteCwd(suffix, ctx.AbsSyncPath(), ctx.GutArgs(args...)...)
}

func (ctx *SyncContext) getPidfileScope() string {
	return strings.Replace(ctx.WatchedRoot(), "/", "_", -1)
}

func (ctx *SyncContext) getPidfilePath(name string) string {
	scopedName := name + "-" + ctx.getPidfileScope()
	return ctx.AbsPath(path.Join(PidfilesPath, scopedName+".pid"))
}

func (ctx *SyncContext) SaveDaemonPid(name string, pid int) (err error) {
	err = ctx.Mkdirp(PidfilesPath)
	if err != nil {
		return err
	}
	return ctx.WriteFile(ctx.getPidfilePath(name), []byte(fmt.Sprintf("%d", pid)))
}

func (ctx *SyncContext) KillViaPidfile(name string) (err error) {
	logger := ctx.Logger()
	pidfilePath := ctx.getPidfilePath(name)
	valStr, err := ctx.ReadFile(pidfilePath)
	if err != nil {
		return err
	}
	pid, err := strconv.ParseInt(string(valStr), 10, 32)
	if err != nil {
		logger.Printf("@(error:Ignoring pidfile for) %s @(error:due to invalid contents [%s])\n", name, string(valStr))
		return nil
	}
	// Is it still (presumably) running?
	_, _, retCode, err := ctx.Run("pgrep", "-F", pidfilePath, name)
	if err != nil {
		return err
	}
	if retCode == 0 {
		logger.Printf("@(dim)Killing %s (pid %d)...@(r)", name, pid)
		_, err = ctx.Quote("pkill", "pkill", "-F", pidfilePath, name)
		if err != nil {
			logger.Printf(" @(error:failed, %s)@(dim:.)\n", err.Error())
		} else {
			logger.Printf(" done@(dim:.)\n")
		}
	}
	ctx.DeleteFile(pidfilePath)
	return nil
}

func (ctx *SyncContext) KillAllViaPidfiles() {
	logger := ctx.Logger()
	if ctx.IsWindows() {
		logger.Bail(errors.New("Not implemented"))
	}
	files, err := ctx.ListDirectory(ctx.AbsPath(PidfilesPath))
	if err != nil {
		logger.Printf("Encountered error while listing pidfiles: %v\n", err)
		return
	}
	pidfileScope := ctx.getPidfileScope()
	for _, filename := range files {
		if !strings.HasSuffix(filename, ".pid") {
			continue
		}
		scopedName := filename[:len(filename)-len(".pid")]
		parts := strings.SplitN(scopedName, "-", 2)
		if len(parts) != 2 {
			continue
		}
		name, scope := parts[0], parts[1]
		if name == "gut" && ctx.IsLocal() {
			// Only kill gut if it's on a different host
			continue
		}
		if scope == "" || scope != pidfileScope {
			continue
		}
		logger.Printf("Killing process via pidfile %s\n", scopedName)
		err := ctx.KillViaPidfile(name)
		if err != nil {
			logger.Printf("Error killing %s process via pidfile: %v\n", name, err)
		}
	}
}
