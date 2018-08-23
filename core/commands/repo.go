package commands

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	oldcmds "github.com/ipfs/go-ipfs/commands"
	lgc "github.com/ipfs/go-ipfs/commands/legacy"
	cmdenv "github.com/ipfs/go-ipfs/core/commands/cmdenv"
	e "github.com/ipfs/go-ipfs/core/commands/e"
	corerepo "github.com/ipfs/go-ipfs/core/corerepo"
	fsrepo "github.com/ipfs/go-ipfs/repo/fsrepo"

	cmdkit "gx/ipfs/QmSP88ryZkHSRn1fnngAaV2Vcn63WUJzAavnRM9CVdU1Ky/go-ipfs-cmdkit"
	cmds "gx/ipfs/QmSwR1QndLsdnw2mpW2rrywoCUPeY7o8UxtK1dLpPCNFGD/go-ipfs-cmds"
	config "gx/ipfs/QmTyiSs9VgdVb4pnzdjtKhcfdTkHFEaNn6xnCbZq4DTFRt/go-ipfs-config"
	cid "gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	bstore "gx/ipfs/QmcmpX42gtDv1fz24kau4wjS9hfwWj5VexWBKgGnWzsyag/go-ipfs-blockstore"
)

type RepoVersion struct {
	Version string
}

var RepoCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Manipulate the IPFS repo.",
		ShortDescription: `
'ipfs repo' is a plumbing command used to manipulate the repo.
`,
	},

	Subcommands: map[string]*cmds.Command{
		"stat":    repoStatCmd,
		"gc":      lgc.NewCommand(repoGcCmd),
		"fsck":    lgc.NewCommand(RepoFsckCmd),
		"version": lgc.NewCommand(repoVersionCmd),
		"verify":  lgc.NewCommand(repoVerifyCmd),
	},
}

// GcResult is the result returned by "repo gc" command.
type GcResult struct {
	Key   *cid.Cid
	Error string `json:",omitempty"`
}

var repoGcCmd = &oldcmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Perform a garbage collection sweep on the repo.",
		ShortDescription: `
'ipfs repo gc' is a plumbing command that will sweep the local
set of stored objects and remove ones that are not pinned in
order to reclaim hard disk space.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption("stream-errors", "Stream errors."),
		cmdkit.BoolOption("quiet", "q", "Write minimal output."),
	},
	Run: func(req oldcmds.Request, res oldcmds.Response) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}

		streamErrors, _, _ := res.Request().Option("stream-errors").Bool()

		gcOutChan := corerepo.GarbageCollectAsync(n, req.Context())

		outChan := make(chan interface{})
		res.SetOutput(outChan)

		go func() {
			defer close(outChan)

			if streamErrors {
				errs := false
				for res := range gcOutChan {
					if res.Error != nil {
						select {
						case outChan <- &GcResult{Error: res.Error.Error()}:
						case <-req.Context().Done():
							return
						}
						errs = true
					} else {
						select {
						case outChan <- &GcResult{Key: res.KeyRemoved}:
						case <-req.Context().Done():
							return
						}
					}
				}
				if errs {
					outChan <- &GcResult{Error: "encountered errors during gc run"}
				}
			} else {
				err := corerepo.CollectResult(req.Context(), gcOutChan, func(k *cid.Cid) {
					select {
					case outChan <- &GcResult{Key: k}:
					case <-req.Context().Done():
					}
				})
				if err != nil {
					res.SetError(err, cmdkit.ErrNormal)
				}
			}
		}()
	},
	Type: GcResult{},
	Marshalers: oldcmds.MarshalerMap{
		oldcmds.Text: func(res oldcmds.Response) (io.Reader, error) {
			v, err := unwrapOutput(res.Output())
			if err != nil {
				return nil, err
			}

			quiet, _, err := res.Request().Option("quiet").Bool()
			if err != nil {
				return nil, err
			}

			obj, ok := v.(*GcResult)
			if !ok {
				return nil, e.TypeErr(obj, v)
			}

			if obj.Error != "" {
				fmt.Fprintf(res.Stderr(), "Error: %s\n", obj.Error)
				return nil, nil
			}

			msg := obj.Key.String() + "\n"
			if !quiet {
				msg = "removed " + msg
			}

			return bytes.NewBufferString(msg), nil
		},
	},
}

var repoStatCmd = &cmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Get stats for the currently used repo.",
		ShortDescription: `
'ipfs repo stat' provides information about the local set of
stored objects. It outputs:

RepoSize        int Size in bytes that the repo is currently taking.
StorageMax      string Maximum datastore size (from configuration)
NumObjects      int Number of objects in the local repo.
RepoPath        string The path to the repo being currently used.
Version         string The repo version.
`,
	},
	Options: []cmdkit.Option{
		cmdkit.BoolOption("size-only", "Only report RepoSize and StorageMax."),
		cmdkit.BoolOption("human", "Output sizes in MiB."),
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		n, err := cmdenv.GetNode(env)
		if err != nil {
			return err
		}

		sizeOnly, _ := req.Options["size-only"].(bool)
		if sizeOnly {
			sizeStat, err := corerepo.RepoSize(req.Context, n)
			if err != nil {
				return err
			}
			cmds.EmitOnce(res, &corerepo.Stat{
				SizeStat: sizeStat,
			})
			return nil
		}

		stat, err := corerepo.RepoStat(req.Context, n)
		if err != nil {
			return err
		}

		return cmds.EmitOnce(res, &stat)
	},
	Type: &corerepo.Stat{},
	Encoders: cmds.EncoderMap{
		cmds.Text: cmds.MakeEncoder(func(req *cmds.Request, w io.Writer, v interface{}) error {
			stat, ok := v.(*corerepo.Stat)
			if !ok {
				return e.TypeErr(stat, v)
			}

			wtr := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
			defer wtr.Flush()

			human, _ := req.Options["human"].(bool)
			sizeOnly, _ := req.Options["size-only"].(bool)

			printSize := func(name string, size uint64) {
				sizeInMiB := size / (1024 * 1024)
				if human && sizeInMiB > 0 {
					fmt.Fprintf(wtr, "%s (MiB):\t%d\n", name, sizeInMiB)
				} else {
					fmt.Fprintf(wtr, "%s:\t%d\n", name, size)
				}
			}

			if !sizeOnly {
				fmt.Fprintf(wtr, "NumObjects:\t%d\n", stat.NumObjects)
			}

			printSize("RepoSize", stat.RepoSize)
			printSize("StorageMax", stat.StorageMax)

			if !sizeOnly {
				fmt.Fprintf(wtr, "RepoPath:\t%s\n", stat.RepoPath)
				fmt.Fprintf(wtr, "Version:\t%s\n", stat.Version)
			}

			return nil
		}),
	},
}

var RepoFsckCmd = &oldcmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Remove repo lockfiles.",
		ShortDescription: `
'ipfs repo fsck' is a plumbing command that will remove repo and level db
lockfiles, as well as the api file. This command can only run when no ipfs
daemons are running.
`,
	},
	Run: func(req oldcmds.Request, res oldcmds.Response) {
		configRoot := req.InvocContext().ConfigRoot

		dsPath, err := config.DataStorePath(configRoot)
		if err != nil {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}

		dsLockFile := filepath.Join(dsPath, "LOCK") // TODO: get this lockfile programmatically
		repoLockFile := filepath.Join(configRoot, fsrepo.LockFile)
		apiFile := filepath.Join(configRoot, "api") // TODO: get this programmatically

		log.Infof("Removing repo lockfile: %s", repoLockFile)
		log.Infof("Removing datastore lockfile: %s", dsLockFile)
		log.Infof("Removing api file: %s", apiFile)

		err = os.Remove(repoLockFile)
		if err != nil && !os.IsNotExist(err) {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}
		err = os.Remove(dsLockFile)
		if err != nil && !os.IsNotExist(err) {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}
		err = os.Remove(apiFile)
		if err != nil && !os.IsNotExist(err) {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}

		res.SetOutput(&MessageOutput{"Lockfiles have been removed.\n"})
	},
	Type: MessageOutput{},
	Marshalers: oldcmds.MarshalerMap{
		oldcmds.Text: MessageTextMarshaler,
	},
}

type VerifyProgress struct {
	Msg      string
	Progress int
}

var repoVerifyCmd = &oldcmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Verify all blocks in repo are not corrupted.",
	},
	Run: func(req oldcmds.Request, res oldcmds.Response) {
		nd, err := req.InvocContext().GetNode()
		if err != nil {
			res.SetError(err, cmdkit.ErrNormal)
			return
		}

		out := make(chan interface{})
		res.SetOutput((<-chan interface{})(out))
		defer close(out)

		bs := bstore.NewBlockstore(nd.Repo.Datastore())
		bs.HashOnRead(true)

		keys, err := bs.AllKeysChan(req.Context())
		if err != nil {
			log.Error(err)
			return
		}

		var fails int
		var i int
		for k := range keys {
			_, err := bs.Get(k)
			if err != nil {
				select {
				case out <- &VerifyProgress{
					Msg: fmt.Sprintf("block %s was corrupt (%s)", k, err),
				}:
				case <-req.Context().Done():
					return
				}
				fails++
			}
			i++
			select {
			case out <- &VerifyProgress{Progress: i}:
			case <-req.Context().Done():
				return
			}
		}

		if fails == 0 {
			select {
			case out <- &VerifyProgress{Msg: "verify complete, all blocks validated."}:
			case <-req.Context().Done():
				return
			}
		} else {
			res.SetError(fmt.Errorf("verify complete, some blocks were corrupt"), cmdkit.ErrNormal)
		}
	},
	Type: &VerifyProgress{},
	Marshalers: oldcmds.MarshalerMap{
		oldcmds.Text: func(res oldcmds.Response) (io.Reader, error) {
			v, err := unwrapOutput(res.Output())
			if err != nil {
				return nil, err
			}

			obj, ok := v.(*VerifyProgress)
			if !ok {
				return nil, e.TypeErr(obj, v)
			}

			buf := new(bytes.Buffer)
			if strings.Contains(obj.Msg, "was corrupt") {
				fmt.Fprintln(os.Stdout, obj.Msg)
				return buf, nil
			}

			if obj.Msg != "" {
				if len(obj.Msg) < 20 {
					obj.Msg += "             "
				}
				fmt.Fprintln(buf, obj.Msg)
				return buf, nil
			}

			fmt.Fprintf(buf, "%d blocks processed.\r", obj.Progress)
			return buf, nil
		},
	},
}

var repoVersionCmd = &oldcmds.Command{
	Helptext: cmdkit.HelpText{
		Tagline: "Show the repo version.",
		ShortDescription: `
'ipfs repo version' returns the current repo version.
`,
	},

	Options: []cmdkit.Option{
		cmdkit.BoolOption("quiet", "q", "Write minimal output."),
	},
	Run: func(req oldcmds.Request, res oldcmds.Response) {
		res.SetOutput(&RepoVersion{
			Version: fmt.Sprint(fsrepo.RepoVersion),
		})
	},
	Type: RepoVersion{},
	Marshalers: oldcmds.MarshalerMap{
		oldcmds.Text: func(res oldcmds.Response) (io.Reader, error) {
			v, err := unwrapOutput(res.Output())
			if err != nil {
				return nil, err
			}
			response, ok := v.(*RepoVersion)
			if !ok {
				return nil, e.TypeErr(response, v)
			}

			quiet, _, err := res.Request().Option("quiet").Bool()
			if err != nil {
				return nil, err
			}

			buf := new(bytes.Buffer)
			if quiet {
				buf = bytes.NewBufferString(fmt.Sprintf("fs-repo@%s\n", response.Version))
			} else {
				buf = bytes.NewBufferString(fmt.Sprintf("ipfs repo version fs-repo@%s\n", response.Version))
			}
			return buf, nil

		},
	},
}
